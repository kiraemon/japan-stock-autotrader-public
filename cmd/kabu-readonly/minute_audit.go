package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/capturehistory"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/streambars"
)

const minuteAuditVersion = "processing-history-minute-audit-v0.1"

type minuteAuditFrame struct {
	Capture    capturehistory.CaptureTiming `json:"source"`
	ObservedAt time.Time                    `json:"processed_observed_at"`
	Outcome    streambars.Outcome           `json:"outcome"`
	State      streambars.State             `json:"state"`
}

type minuteAuditReport struct {
	Version           string             `json:"version"`
	Converter         string             `json:"converter"`
	ClockModel        string             `json:"clock_model"`
	Date              string             `json:"date_jst"`
	MinuteStart       time.Time          `json:"minute_start"`
	Deadline          time.Time          `json:"processing_deadline"`
	StartedAt         time.Time          `json:"started_at"`
	PreparedAt        time.Time          `json:"prepared_at_before_report_save"`
	ErrorCode         string             `json:"error_code,omitempty"`
	Frames            []minuteAuditFrame `json:"frames"`
	ForwardTest       bool               `json:"forward_test"`
	StrategyEligible  bool               `json:"strategy_eligible"`
	ExecutionIncluded bool               `json:"execution_included"`
}

// The command composes the independent capture, conversion and candidate slices.
// No replayed finished_at is substituted for the actual processing clock.
func newMinuteAudit(date, environment string, symbols []string, directory string, now func() time.Time) (func(context.Context, capturehistory.AlignedRound) (string, error), error) {
	feeds := map[string]*streambars.Feed{}
	for _, symbol := range symbols {
		feed, err := streambars.New(streambars.Config{Date: date, Symbol: symbol, Environment: environment})
		if err != nil {
			return nil, err
		}
		feeds[symbol] = feed
	}
	previous := time.Time{}
	halted := false
	return func(ctx context.Context, round capturehistory.AlignedRound) (string, error) {
		if halted {
			return "", errors.New("停止した履歴検査は再開しません")
		}
		r := minuteAuditReport{Version: minuteAuditVersion, Converter: buildbars.Version, ClockModel: "processing_clock_not_archive_replay", Date: date, MinuteStart: round.MinuteStart, Deadline: round.Deadline, StartedAt: now(), Frames: []minuteAuditFrame{}}
		last := round.RawCompletedAt
		if !round.RawCompleted || !round.RawOnTime || round.Date != date || string(round.Environment) != environment || len(round.Captures) != len(feeds) {
			r.ErrorCode = "round_contract_invalid"
		} else if r.StartedAt.Before(last) || !previous.IsZero() && r.StartedAt.Before(previous) {
			r.ErrorCode = "observation_clock_reversed"
		} else if !r.StartedAt.Before(round.Deadline) {
			r.ErrorCode = "processing_deadline_exceeded"
		}
		seen := map[string]bool{}
		for _, source := range round.Captures {
			if r.ErrorCode != "" {
				break
			}
			if ctx.Err() != nil {
				r.ErrorCode = "processing_interrupted"
				break
			}
			feed := feeds[source.Symbol]
			if feed == nil || seen[source.Symbol] || !source.Complete || filepath.Base(source.File) != source.File {
				r.ErrorCode = "source_contract_invalid"
				break
			}
			seen[source.Symbol] = true
			raw, err := readMinuteOriginal(filepath.Join(directory, source.File))
			if err != nil {
				r.ErrorCode = "source_read_failed"
				break
			}
			sum := sha256.Sum256(raw)
			if int64(len(raw)) != source.Bytes || hex.EncodeToString(sum[:]) != source.SHA256 {
				r.ErrorCode = "source_hash_or_size_changed"
				break
			}
			dataset, err := buildbars.FromHistory(ctx, raw)
			if err != nil {
				r.ErrorCode = "minute_conversion_failed"
				break
			}
			observed := now()
			if observed.Before(last) || observed.Before(r.StartedAt) {
				r.ErrorCode = "observation_clock_reversed"
				break
			}
			last = observed
			result := feed.Offer(observed, dataset)
			state := feed.Snapshot()
			r.Frames = append(r.Frames, minuteAuditFrame{source, observed, result, state})
			switch {
			case !result.Accepted:
				r.ErrorCode = result.Reason
			case observed.After(round.Deadline):
				r.ErrorCode = "processing_deadline_exceeded"
			case !state.NextStart.Equal(round.MinuteStart.Add(time.Minute)) || len(result.Candidates) != 1:
				r.ErrorCode = "expected_minute_not_enumerated"
			}
		}
		r.PreparedAt = now()
		if r.PreparedAt.Before(last) || r.PreparedAt.Before(r.StartedAt) {
			r.ErrorCode = "observation_clock_reversed"
		}
		if r.ErrorCode == "" && r.PreparedAt.After(round.Deadline) {
			r.ErrorCode = "processing_deadline_exceeded"
		}
		if r.ErrorCode == "" && ctx.Err() != nil {
			r.ErrorCode = "processing_interrupted"
		}
		path, err := saveMinuteAudit(directory, r)
		previous = r.PreparedAt
		halted = err != nil || r.ErrorCode != ""
		if err != nil {
			return "", err
		}
		if r.ErrorCode != "" {
			return filepath.Base(path), fmt.Errorf("履歴分足検査で停止しました: reason=%s", r.ErrorCode)
		}
		return filepath.Base(path), nil
	}, nil
}

func readMinuteOriginal(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("原履歴を読み込めません")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(raw) > 64<<20 {
		return nil, errors.New("原履歴の読込またはサイズを確認してください")
	}
	return raw, nil
}

func saveMinuteAudit(directory string, report minuteAuditReport) (string, error) {
	f, err := os.CreateTemp(directory, ".minute-audit-*.tmp")
	if err != nil {
		return "", errors.New("履歴分足検査の保存先を作成できません")
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err := json.NewEncoder(f).Encode(report); err != nil {
		f.Close()
		return "", errors.New("履歴分足検査を生成できません")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", errors.New("履歴分足検査を同期できません")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("履歴分足検査を閉じられません")
	}
	path := filepath.Join(directory, strings.TrimPrefix(strings.TrimSuffix(filepath.Base(temporary), ".tmp"), ".")+".json")
	if os.Rename(temporary, path) != nil {
		return "", errors.New("履歴分足検査を確定できません")
	}
	return path, nil
}
