// Package inspectquality inspects saved snapshots without API or credential access.
package inspectquality

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Range struct {
	Count int     `json:"count"`
	Min   float64 `json:"min_seconds"`
	Max   float64 `json:"max_seconds"`
}

func (r *Range) add(value float64) {
	if r.Count == 0 || value < r.Min {
		r.Min = value
	}
	if r.Count == 0 || value > r.Max {
		r.Max = value
	}
	r.Count++
}

// Report deliberately contains aggregate quality metadata only, never raw data,
// endpoint strings, errors from input, prices, balances, or holding details.
type Report struct {
	Version                   int        `json:"report_version"`
	Environment               string     `json:"environment"`
	Files                     int        `json:"files"`
	ValidFiles                int        `json:"valid_files"`
	InvalidFiles              int        `json:"invalid_files"`
	UnsupportedSchemaFiles    int        `json:"unsupported_schema_files"`
	EnvironmentMismatchFiles  int        `json:"environment_mismatch_files"`
	SourceBytes               int64      `json:"source_bytes"`
	IncompleteFiles           int        `json:"incomplete_files"`
	InconsistentCompleteFiles int        `json:"inconsistent_complete_files"`
	FilesMissingRequiredAPIs  int        `json:"files_missing_required_apis"`
	Records                   int        `json:"records"`
	APIErrorRecords           int        `json:"api_error_records"`
	InvalidDataRecords        int        `json:"invalid_data_records"`
	UnknownEndpointRecords    int        `json:"unknown_endpoint_records"`
	DuplicateEndpointRecords  int        `json:"duplicate_endpoint_records"`
	InvalidReceiveTimes       int        `json:"invalid_receive_times"`
	BoardRecords              int        `json:"board_records"`
	MissingPrices             int        `json:"missing_prices"`
	NonPositivePrices         int        `json:"non_positive_prices"`
	MissingMarketTimes        int        `json:"missing_market_times"`
	RepeatedMarketTimes       int        `json:"repeated_market_times"`
	BackwardMarketTimes       int        `json:"backward_market_times"`
	MarketTimeAheadOfReceive  int        `json:"market_time_ahead_of_receive"`
	OverlappingSnapshots      int        `json:"overlapping_snapshots"`
	FirstStartedAt            *time.Time `json:"first_started_at,omitempty"`
	LastFinishedAt            *time.Time `json:"last_finished_at,omitempty"`
	PostCollectionGap         Range      `json:"post_collection_gap"`
	PriceAgeAtReceive         Range      `json:"price_age_at_receive"`
	NeedsReview               bool       `json:"needs_review"`
}

// This is a quality projection of schema 1, separate from the collector's types.
// Account data is retained only briefly as raw JSON to check the outer shape.
type snapshotInput struct {
	Version     int           `json:"schema_version"`
	Environment string        `json:"environment"`
	StartedAt   time.Time     `json:"started_at"`
	FinishedAt  time.Time     `json:"finished_at"`
	Complete    bool          `json:"complete"`
	Records     []recordInput `json:"records"`
}
type recordInput struct {
	Endpoint   string          `json:"endpoint"`
	ReceivedAt time.Time       `json:"received_at"`
	Data       json.RawMessage `json:"data"`
	Error      string          `json:"error"`
}
type boardInput struct {
	Price      *float64   `json:"CurrentPrice"`
	MarketTime *time.Time `json:"CurrentPriceTime"`
}
type boardObservation struct {
	endpoint string
	received time.Time
	market   time.Time
}
type runObservation struct{ started, finished time.Time }

// Inspect accepts at most 10000 snapshot files, each at most 64 MiB. Invalid
// files become findings; directory failures and cancellation are fatal.
func Inspect(ctx context.Context, directory, environment string) (Report, error) {
	r := Report{Version: 1, Environment: environment}
	if environment != "test" && environment != "production" {
		return r, errors.New("接続環境はtestまたはproductionを指定してください")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return r, errors.New("保存データのディレクトリーを開けません")
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "snapshot-") && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	if len(paths) == 0 || len(paths) > 10000 {
		return r, errors.New("対象スナップショットは1〜10000ファイル必要です")
	}
	r.Files = len(paths)
	var runs []runObservation
	var boards []boardObservation
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return r, errors.New("品質確認を中断しました")
		}
		s, size, err := readSnapshot(path)
		r.SourceBytes += size
		if err != nil {
			r.InvalidFiles++
			continue
		}
		if s.Version != 1 {
			r.UnsupportedSchemaFiles++
			r.InvalidFiles++
			continue
		}
		if s.Environment != environment {
			r.EnvironmentMismatchFiles++
			r.InvalidFiles++
			continue
		}
		if s.StartedAt.IsZero() || s.FinishedAt.IsZero() || s.FinishedAt.Before(s.StartedAt) || len(s.Records) == 0 {
			r.InvalidFiles++
			continue
		}
		r.ValidFiles++
		if !s.Complete {
			r.IncompleteFiles++
		}
		runs = append(runs, runObservation{s.StartedAt, s.FinishedAt})
		seen := map[string]bool{}
		hasBoard, hasError := false, false
		lastReceived := s.StartedAt
		for _, record := range s.Records {
			r.Records++
			if record.ReceivedAt.IsZero() || record.ReceivedAt.Before(lastReceived) || record.ReceivedAt.After(s.FinishedAt) {
				r.InvalidReceiveTimes++
			}
			lastReceived = record.ReceivedAt
			if seen[record.Endpoint] {
				r.DuplicateEndpointRecords++
			}
			seen[record.Endpoint] = true
			isBoard := strings.HasPrefix(record.Endpoint, "/board/")
			if isBoard {
				hasBoard = true
				r.BoardRecords++
			}
			if record.Error != "" {
				r.APIErrorRecords++
				hasError = true
				continue
			}
			data := bytes.TrimSpace(record.Data)
			want := byte('{')
			switch record.Endpoint {
			case "/positions?product=1&addinfo=true", "/positions?product=2&addinfo=true":
				want = '['
			case "/wallet/cash", "/wallet/margin":
			default:
				if !isBoard {
					r.UnknownEndpointRecords++
					continue
				}
			}
			if len(data) == 0 || data[0] != want {
				r.InvalidDataRecords++
				continue
			}
			if isBoard {
				var board boardInput
				if json.Unmarshal(data, &board) != nil {
					r.InvalidDataRecords++
					continue
				}
				if board.Price == nil {
					r.MissingPrices++
				} else if *board.Price <= 0 {
					r.NonPositivePrices++
				}
				if board.MarketTime == nil || board.MarketTime.IsZero() {
					r.MissingMarketTimes++
					continue
				}
				if !record.ReceivedAt.IsZero() {
					age := record.ReceivedAt.Sub(*board.MarketTime).Seconds()
					if age < 0 {
						r.MarketTimeAheadOfReceive++
					} else {
						r.PriceAgeAtReceive.add(age)
					}
					boards = append(boards, boardObservation{record.Endpoint, record.ReceivedAt, *board.MarketTime})
				}
			}
		}
		missing := !hasBoard
		for _, endpoint := range []string{"/wallet/cash", "/wallet/margin", "/positions?product=1&addinfo=true", "/positions?product=2&addinfo=true"} {
			if !seen[endpoint] {
				missing = true
			}
		}
		if missing {
			r.FilesMissingRequiredAPIs++
		}
		if s.Complete && (hasError || missing) {
			r.InconsistentCompleteFiles++
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].started.Before(runs[j].started) })
	if len(runs) > 0 {
		first, last := runs[0].started, runs[0].finished
		r.FirstStartedAt = &first
		for i, run := range runs {
			if i > 0 {
				gap := run.started.Sub(last).Seconds()
				if gap < 0 {
					r.OverlappingSnapshots++
				} else {
					r.PostCollectionGap.add(gap)
				}
			}
			if run.finished.After(last) {
				last = run.finished
			}
		}
		r.LastFinishedAt = &last
	}
	sort.SliceStable(boards, func(i, j int) bool { return boards[i].received.Before(boards[j].received) })
	previous := map[string]time.Time{}
	for _, board := range boards {
		if last, ok := previous[board.endpoint]; ok {
			if board.market.Equal(last) {
				r.RepeatedMarketTimes++
			}
			if board.market.Before(last) {
				r.BackwardMarketTimes++
			}
		}
		previous[board.endpoint] = board.market
	}
	r.NeedsReview = r.InvalidFiles+r.IncompleteFiles+r.InconsistentCompleteFiles+r.FilesMissingRequiredAPIs+r.APIErrorRecords+r.InvalidDataRecords+r.UnknownEndpointRecords+r.DuplicateEndpointRecords+r.InvalidReceiveTimes+r.MissingPrices+r.NonPositivePrices+r.MissingMarketTimes+r.BackwardMarketTimes+r.MarketTimeAheadOfReceive+r.OverlappingSnapshots > 0
	return r, nil
}

func readSnapshot(path string) (snapshotInput, int64, error) {
	var s snapshotInput
	f, err := os.Open(path)
	if err != nil {
		return s, 0, errors.New("snapshot unreadable")
	}
	defer f.Close()
	const limit = 64 << 20
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return s, 0, errors.New("snapshot not a regular file")
	}
	if info.Size() > limit {
		return s, info.Size(), errors.New("snapshot oversized")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return s, int64(len(raw)), errors.New("snapshot oversized or unreadable")
	}
	if json.Unmarshal(raw, &s) != nil {
		return s, int64(len(raw)), errors.New("snapshot invalid")
	}
	return s, int64(len(raw)), nil
}

func Save(directory string, report Report) (string, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", errors.New("品質レポートの保存先を作成できません")
	}
	f, err := os.CreateTemp(directory, ".quality-*.tmp")
	if err != nil {
		return "", errors.New("品質レポートを作成できません")
	}
	temp := f.Name()
	defer os.Remove(temp)
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		f.Close()
		return "", errors.New("品質レポートを生成できません")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", errors.New("品質レポートを書き込めません")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("品質レポートを閉じられません")
	}
	name := "quality-" + report.Environment + "-" + strings.TrimSuffix(strings.TrimPrefix(filepath.Base(temp), ".quality-"), ".tmp") + ".json"
	final := filepath.Join(directory, name)
	if err := os.Rename(temp, final); err != nil {
		return "", errors.New("品質レポートを確定できません")
	}
	return final, nil
}
