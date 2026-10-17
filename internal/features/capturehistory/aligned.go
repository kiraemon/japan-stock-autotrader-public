package capturehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

const AlignedVersion = "aligned-history-capture-v0.1"
const AlignedMinutes = 111
const AlignedOffset = 2 * time.Second
const AlignedDeadline = 10 * time.Second

type Clock interface {
	Now() time.Time
	WaitUntil(context.Context, time.Time) error
}

type WallClock struct{}

func (WallClock) Now() time.Time { return time.Now().UTC() }
func (WallClock) WaitUntil(ctx context.Context, at time.Time) error {
	previous := time.Now().UTC()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := time.Now().UTC()
		if now.Before(previous) {
			return errors.New("毎分履歴の待機中に時計逆行を検出しました")
		}
		previous = now
		remaining := at.Sub(now)
		if remaining <= 0 {
			return nil
		}
		// Recheck wall time after clock corrections; do not sleep through a
		// backwards adjustment or rely on a stale monotonic target duration.
		if remaining > time.Second {
			remaining = time.Second
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type CaptureTiming struct {
	Symbol         string     `json:"symbol"`
	File           string     `json:"archive_file"`
	SHA256         string     `json:"archive_sha256"`
	StartedAt      time.Time  `json:"started_at"`
	ReceivedAt     *time.Time `json:"history_received_at,omitempty"`
	HTTPFinishedAt time.Time  `json:"http_finished_at"`
	SavedAt        time.Time  `json:"raw_saved_at"`
	Bytes          int64      `json:"archive_bytes"`
	Complete       bool       `json:"http_complete"`
}

type AlignedRound struct {
	Version            string           `json:"version"`
	Date               string           `json:"date_jst"`
	Environment        kabu.Environment `json:"environment"`
	Symbols            []string         `json:"requested_symbols"`
	MinuteStart        time.Time        `json:"minute_start"`
	ScheduledAt        time.Time        `json:"scheduled_at"`
	Deadline           time.Time        `json:"raw_deadline"`
	StartedAt          time.Time        `json:"actual_started_at"`
	RawCompletedAt     time.Time        `json:"raw_completed_at"`
	StartDelayNS       int64            `json:"start_delay_ns"`
	RawCompleted       bool             `json:"raw_completed"`
	RawOnTime          bool             `json:"raw_on_time"`
	ErrorCode          string           `json:"error_code,omitempty"`
	ConversionIncluded bool             `json:"conversion_included"`
	ForwardTest        bool             `json:"forward_test"`
	StrategyEligible   bool             `json:"strategy_eligible"`
	Captures           []CaptureTiming  `json:"captures"`
}

type AlignedOptions struct {
	Date        string
	Environment kabu.Environment
	Symbols     []string
	Directory   string
	Clock       Clock
	Observe     func(string) error
	AfterRound  func(context.Context) error
}

// ValidateAligned runs before opening a connection or reading credentials.
// No calendar or automatic next-day rollover is inferred from the date.
func ValidateAligned(date string, symbols []string, now time.Time) error {
	parsed, err := time.ParseInLocation("2006-01-02", date, marketdata.JST)
	first := parsed.Add(9*time.Hour + time.Minute)
	if err != nil || parsed.Format("2006-01-02") != date || now.IsZero() || !now.Before(first.Add(AlignedDeadline)) || AlignedEnd(date).Sub(now) > 24*time.Hour || len(symbols) < 1 || len(symbols) > 3 {
		return errors.New("毎分履歴は24時間以内の開始日・1〜3銘柄を指定してください。最初の締切後は開始しません")
	}
	seen := map[string]bool{}
	for _, symbol := range symbols {
		if kabu.ValidateSymbol(symbol) != nil || seen[symbol] {
			return errors.New("毎分履歴の銘柄・重複を確認してください")
		}
		seen[symbol] = true
	}
	return nil
}

func AlignedEnd(date string) time.Time { return marketdata.Clock(date, 10, 52) }

// RunAligned collects raw responses, not certified candles or strategy input.
// One history round precedes one optional ordinary snapshot, on the same owner.
// A deadline miss is terminal: missed minutes are never fetched in a burst.
func RunAligned(ctx context.Context, reader Reader, options AlignedOptions, out io.Writer) error {
	clock := options.Clock
	if clock == nil {
		clock = WallClock{}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if options.Environment != kabu.Test && options.Environment != kabu.Production {
		return errors.New("毎分履歴の環境を確認してください")
	}
	if err := ValidateAligned(options.Date, options.Symbols, clock.Now()); err != nil {
		return err
	}
	lastClock := clock.Now()
	phase := func(value string) error {
		if options.Observe != nil {
			return options.Observe(value)
		}
		return nil
	}
	start := marketdata.Clock(options.Date, 9, 0)
	for i := 0; i < AlignedMinutes; i++ {
		minute := start.Add(time.Duration(i) * time.Minute)
		scheduled := minute.Add(time.Minute + AlignedOffset)
		deadline := minute.Add(time.Minute + AlignedDeadline)
		if err := phase("waiting"); err != nil {
			return err
		}
		if err := clock.WaitUntil(ctx, scheduled); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		now := clock.Now()
		r := AlignedRound{Version: AlignedVersion, Date: options.Date, Environment: options.Environment, Symbols: append([]string{}, options.Symbols...), MinuteStart: minute, ScheduledAt: scheduled, Deadline: deadline, StartedAt: now, StartDelayNS: now.Sub(scheduled).Nanoseconds(), Captures: []CaptureTiming{}}
		var runErr error
		switch {
		case now.Before(lastClock) || now.Before(scheduled):
			r.ErrorCode = "observation_clock_reversed"
			runErr = errors.New("毎分履歴の時計逆行を検出しました")
		case !now.Before(deadline):
			r.ErrorCode = "start_deadline_exceeded"
			runErr = errors.New("毎分履歴の開始締切を超えました")
		default:
			if err := phase("collecting"); err != nil {
				return err
			}
			captureCtx, cancel := context.WithTimeout(ctx, deadline.Sub(now))
			runErr = capture(captureCtx, reader, options.Environment, options.Symbols, options.Directory, out, clock.Now, func(c CaptureTiming) error {
				r.Captures = append(r.Captures, c)
				if c.StartedAt.Before(now) || c.HTTPFinishedAt.Before(c.StartedAt) || c.SavedAt.Before(c.HTTPFinishedAt) || c.ReceivedAt != nil && (c.ReceivedAt.Before(c.StartedAt) || c.ReceivedAt.After(c.HTTPFinishedAt)) {
					r.ErrorCode = "observation_clock_reversed"
					return errors.New("毎分履歴の時計逆行を検出しました")
				}
				now = c.SavedAt
				// Check every symbol, so a slow first response does not start the
				// next symbol after the deadline. The saved original stays intact.
				if c.SavedAt.After(deadline) || c.SavedAt.Equal(deadline) && len(r.Captures) < len(options.Symbols) {
					r.ErrorCode = "raw_deadline_exceeded"
					return errors.New("毎分履歴の原保存締切を超えました")
				}
				return nil
			})
			if captureCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil && r.ErrorCode == "" {
				r.ErrorCode = "raw_deadline_exceeded"
			}
			cancel()
			if runErr != nil && r.ErrorCode == "" {
				r.ErrorCode = "capture_failed"
			}
		}
		r.RawCompletedAt = clock.Now()
		if r.RawCompletedAt.Before(now) || r.RawCompletedAt.Before(lastClock) {
			r.ErrorCode = "observation_clock_reversed"
			runErr = errors.New("毎分履歴の時計逆行を検出しました")
		}
		if runErr == nil && r.RawCompletedAt.After(deadline) {
			r.ErrorCode = "raw_deadline_exceeded"
			runErr = errors.New("毎分履歴の原保存締切を超えました")
		}
		r.RawCompleted = runErr == nil && len(r.Captures) == len(options.Symbols)
		r.RawOnTime = r.RawCompleted && !r.RawCompletedAt.After(deadline)
		path, err := saveAlignedRound(options.Directory, r)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "毎分履歴: 保存=%s / raw_complete=%t / raw_on_time=%t / reason=%s\n", path, r.RawCompleted, r.RawOnTime, r.ErrorCode); err != nil {
			return errors.New("毎分履歴の結果を出力できません")
		}
		if runErr != nil {
			return runErr
		}
		lastClock = r.RawCompletedAt
		if options.AfterRound != nil {
			if err := options.AfterRound(ctx); err != nil {
				return err
			}
		}
		if now := clock.Now(); now.Before(lastClock) {
			return errors.New("通常収集後に時計逆行を検出しました")
		} else {
			lastClock = now
		}
	}
	return nil
}

func saveAlignedRound(directory string, round AlignedRound) (string, error) {
	if os.MkdirAll(directory, 0700) != nil {
		return "", errors.New("毎分履歴の記録先を作成できません")
	}
	f, err := os.CreateTemp(directory, ".aligned-*.tmp")
	if err != nil {
		return "", errors.New("毎分履歴の記録を作成できません")
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if json.NewEncoder(f).Encode(round) != nil {
		f.Close()
		return "", errors.New("毎分履歴の記録を生成できません")
	}
	if f.Sync() != nil {
		f.Close()
		return "", errors.New("毎分履歴の記録を同期できません")
	}
	if f.Close() != nil {
		return "", errors.New("毎分履歴の記録を閉じられません")
	}
	path := filepath.Join(directory, strings.TrimPrefix(strings.TrimSuffix(filepath.Base(temporary), ".tmp"), ".")+".json")
	if err := os.Rename(temporary, path); err != nil {
		return "", fileFinalizeError("毎分履歴の記録を確定できません", err)
	}
	return path, nil
}
