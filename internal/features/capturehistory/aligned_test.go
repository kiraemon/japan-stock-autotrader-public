package capturehistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

type alignedTestClock struct {
	at     time.Time
	waits  []time.Time
	onWait func(*alignedTestClock, time.Time) error
	onNow  func(*alignedTestClock)
}

func (c *alignedTestClock) Now() time.Time {
	if c.onNow != nil {
		c.onNow(c)
	}
	return c.at
}
func (c *alignedTestClock) WaitUntil(ctx context.Context, at time.Time) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.waits = append(c.waits, at)
	if c.onWait != nil {
		return c.onWait(c, at)
	}
	if c.at.Before(at) {
		c.at = at
	}
	return nil
}

type alignedTestReader struct {
	clock       *alignedTestClock
	cost        time.Duration
	calls       int
	failAt      int
	clockBackAt int
}

func (r *alignedTestReader) step() error {
	r.calls++
	r.clock.at = r.clock.at.Add(r.cost)
	if r.calls == r.clockBackAt {
		r.clock.at = r.clock.at.Add(-time.Minute)
	}
	if r.calls == r.failAt {
		return errors.New("synthetic reader failure")
	}
	return nil
}
func (r *alignedTestReader) Board(ctx context.Context, symbol string) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := r.step(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]any{"Symbol": strings.Split(symbol, "@")[0], "Exchange": 1, "TradingVolume": 30})
	return raw, nil
}
func (r *alignedTestReader) TimeAndSales(ctx context.Context, symbol string) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := r.step(); err != nil {
		return nil, err
	}
	// Two equal-time trades are distinct rows, never deduplicated.
	at := r.clock.at.Truncate(time.Minute).Add(-time.Minute)
	raw, _ := json.Marshal(map[string]any{"Symbol": strings.Split(symbol, "@")[0], "Exchange": 1, "TradingPriceCount": 2, "TradingPrice": []any{map[string]any{"Time": at, "Price": 100, "Volume": 10}, map[string]any{"Time": at, "Price": 100, "Volume": 20}}})
	return raw, nil
}

func alignedOptions(t *testing.T) (AlignedOptions, *alignedTestClock, *alignedTestReader) {
	t.Helper()
	date := "2026-10-19"
	c := &alignedTestClock{at: marketdata.Clock(date, 9, 0)}
	r := &alignedTestReader{clock: c, cost: 250 * time.Millisecond}
	return AlignedOptions{Date: date, Environment: "test", Symbols: []string{"7203@1", "8306@1", "6501@1"}, Directory: t.TempDir(), Clock: c}, c, r
}

func rounds(t *testing.T, dir string) []AlignedRound {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "aligned-*.json"))
	result := []AlignedRound{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		var round AlignedRound
		if err != nil || json.Unmarshal(raw, &round) != nil {
			t.Fatal("round record")
		}
		result = append(result, round)
	}
	return result
}

func TestAlignedClockMaintainsMinuteBoundariesAndArchiveProvenance(t *testing.T) {
	options, c, r := alignedOptions(t)
	normal := 0
	options.AfterRound = func(context.Context) error { normal++; c.at = c.at.Add(3 * time.Second); return nil }
	if err := RunAligned(context.Background(), r, options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if normal != 111 || len(c.waits) != 111 || r.calls != 111*9 {
		t.Fatal("wrong count or duplicate bootstrap", normal, len(c.waits), r.calls)
	}
	for i, at := range c.waits {
		want := marketdata.Clock(options.Date, 9, 0).Add(time.Duration(i+1)*time.Minute + 2*time.Second)
		if !at.Equal(want) {
			t.Fatal("completion drift", at, want)
		}
	}
	reports := rounds(t, options.Directory)
	if len(reports) != 111 {
		t.Fatal("round count")
	}
	for _, report := range reports {
		if !report.RawCompleted || !report.RawOnTime || report.ErrorCode != "" || report.StartDelayNS != 0 || len(report.Captures) != 3 || report.ConversionIncluded || report.ForwardTest || report.StrategyEligible {
			t.Fatal("promoted or invalid round", report)
		}
		for _, capture := range report.Captures {
			path := filepath.Join(options.Directory, capture.File)
			raw, err := os.ReadFile(path)
			var original Archive
			if err != nil || json.Unmarshal(raw, &original) != nil || int64(len(raw)) != capture.Bytes || !original.Complete || original.Analysis.Rows != 2 || original.Version != 1 || !original.StartedAt.Equal(capture.StartedAt) || !original.HistoryReceivedAt.Equal(*capture.ReceivedAt) {
				t.Fatal("original provenance")
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != capture.SHA256 {
				t.Fatal("raw file hash lost")
			}
		}
	}
	t.Log("synthetic=true / rounds=111 / history_archives=333 / normal_callbacks=111 / completion_drift=false")
}

func TestAlignedFailuresNeverCatchUpOrStartNextSymbol(t *testing.T) {
	for _, kind := range []string{"late_start", "slow_capture", "slow_save", "reader_failure", "clock_back", "normal_late", "normal_error", "observer_failure"} {
		t.Run(kind, func(t *testing.T) {
			options, c, r := alignedOptions(t)
			normal := 0
			want := "capture_failed"
			options.AfterRound = func(context.Context) error { normal++; return nil }
			switch kind {
			case "late_start":
				c.onWait = func(c *alignedTestClock, at time.Time) error { c.at = at.Add(9 * time.Second); return nil }
				want = "start_deadline_exceeded"
			case "slow_capture":
				r.cost = 3 * time.Second
				want = "raw_deadline_exceeded"
			case "slow_save":
				afterHTTP := 0
				c.onNow = func(c *alignedTestClock) {
					if r.calls == 3 {
						afterHTTP++
						if afterHTTP == 2 {
							c.at = c.at.Add(9 * time.Second)
						}
					}
				}
				want = "raw_deadline_exceeded"
			case "reader_failure":
				r.failAt = 2
			case "clock_back":
				r.clockBackAt = 2
				want = "observation_clock_reversed"
			case "normal_late":
				options.AfterRound = func(context.Context) error { normal++; c.at = c.at.Add(3 * time.Minute); return nil }
				want = "start_deadline_exceeded"
			case "normal_error":
				options.AfterRound = func(context.Context) error { normal++; return errors.New("synthetic normal failure") }
			case "observer_failure":
				options.Observe = func(string) error { return errors.New("synthetic status failure") }
			}
			if err := RunAligned(context.Background(), r, options, &bytes.Buffer{}); err == nil {
				t.Fatal("failure accepted")
			}
			reports := rounds(t, options.Directory)
			switch kind {
			case "observer_failure":
				if r.calls != 0 || len(reports) != 0 {
					t.Fatal("read after observer failure")
				}
			case "normal_error":
				if len(reports) != 1 || normal != 1 || r.calls != 9 {
					t.Fatal("normal failure retried")
				}
			case "normal_late":
				if len(reports) != 2 || normal != 1 || r.calls != 9 {
					t.Fatal("missed minutes fetched in a burst")
				}
				found := false
				for _, v := range reports {
					found = found || v.ErrorCode == want
				}
				if !found {
					t.Fatal("late round missing")
				}
			default:
				if len(reports) != 1 || reports[0].RawOnTime || reports[0].ErrorCode != want || normal != 0 || r.calls > 3 {
					t.Fatal("failure record, retries or next symbol", reports, r.calls)
				}
			}
		})
	}
}

func TestAlignedValidationAndCancellationBeforeReads(t *testing.T) {
	options, c, r := alignedOptions(t)
	for _, date := range []string{"bad", "2026-10-15", "2026-10-22"} {
		if ValidateAligned(date, options.Symbols, c.at) == nil {
			t.Fatal("invalid date accepted")
		}
	}
	if ValidateAligned(options.Date, append(options.Symbols, "9433@1"), c.at) == nil || ValidateAligned(options.Date, []string{"7203@1", "7203@1"}, c.at) == nil {
		t.Fatal("symbol limits")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.onWait = func(_ *alignedTestClock, _ time.Time) error { cancel(); return ctx.Err() }
	if !errors.Is(RunAligned(ctx, r, options, &bytes.Buffer{}), context.Canceled) || r.calls != 0 {
		t.Fatal("cancelled wait read API")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if !errors.Is((WallClock{}).WaitUntil(ctx, time.Now().Add(time.Hour)), context.Canceled) {
		t.Fatal("real wait ignored cancellation")
	}
}

func TestAlignedDeadlineEqualityOnlyAllowsTheFinalSavedSymbol(t *testing.T) {
	for _, oneSymbol := range []bool{true, false} {
		options, c, r := alignedOptions(t)
		if oneSymbol {
			options.Symbols = options.Symbols[:1]
		}
		afterHTTP := 0
		c.onNow = func(c *alignedTestClock) {
			if r.calls == 3 {
				afterHTTP++
				if afterHTTP == 2 {
					c.at = c.at.Add(7250 * time.Millisecond)
				}
			}
		}
		options.AfterRound = func(context.Context) error { return errors.New("synthetic stop after round") }
		if RunAligned(context.Background(), r, options, &bytes.Buffer{}) == nil {
			t.Fatal("expected terminal test condition")
		}
		reports := rounds(t, options.Directory)
		if len(reports) != 1 || r.calls != 3 || reports[0].RawOnTime != oneSymbol || reports[0].RawCompleted != oneSymbol {
			t.Fatal("zero-budget next request or boundary mismatch", reports, r.calls)
		}
	}
}

func TestAlignedSaveFailureAndPerRoundRequestDeadline(t *testing.T) {
	options, _, r := alignedOptions(t)
	blocked := filepath.Join(options.Directory, "blocked")
	if os.WriteFile(blocked, []byte("synthetic"), 0600) != nil {
		t.Fatal("fixture")
	}
	options.Directory = blocked
	if RunAligned(context.Background(), r, options, &bytes.Buffer{}) == nil || r.calls != 3 {
		t.Fatal("save failure not terminal")
	}
	options, _, r = alignedOptions(t)
	options.AfterRound = func(context.Context) error { return errors.New("stop after checking deadline") }
	reader := &alignedDeadlineReader{alignedTestReader: r, t: t}
	if RunAligned(context.Background(), reader, options, &bytes.Buffer{}) == nil || reader.checked != 9 {
		t.Fatal("deadline not applied")
	}
}

type alignedDeadlineReader struct {
	*alignedTestReader
	t       *testing.T
	checked int
}

func (r *alignedDeadlineReader) check(ctx context.Context) {
	r.t.Helper()
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > 8*time.Second || time.Until(d) <= 0 {
		r.t.Fatal("missing per-round request budget")
	}
	r.checked++
}
func (r *alignedDeadlineReader) Board(ctx context.Context, s string) (json.RawMessage, error) {
	r.check(ctx)
	return r.alignedTestReader.Board(ctx, s)
}
func (r *alignedDeadlineReader) TimeAndSales(ctx context.Context, s string) (json.RawMessage, error) {
	r.check(ctx)
	return r.alignedTestReader.TimeAndSales(ctx, s)
}
