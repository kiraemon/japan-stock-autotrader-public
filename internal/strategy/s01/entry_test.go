package s01

import (
	"context"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"reflect"
	"testing"
	"time"
)

func entryBars() []marketdata.Bar {
	bars := []marketdata.Bar{}
	for at := marketdata.Clock("2026-06-30", 9, 5); !at.After(marketdata.Clock("2026-06-30", 10, 50)); at = at.Add(time.Minute) {
		v := int64(100)
		bars = append(bars, marketdata.Bar{Start: at, Open: 100, High: 110, Low: 90, Close: 100, Volume: &v})
	}
	return bars
}
func TestFirstBreakUsesCloseDirectionAndNextMinute(t *testing.T) {
	for _, direction := range []Direction{Long, Short} {
		bars := entryBars()
		opening := Range{100, 110, 90, 105}
		if direction == Short {
			opening.Close = 95
		}
		// Wicks and exact touches do not trigger; opposite breaks do not enter.
		bars[0].High = 120
		bars[0].Low = 80
		bars[1].Close = 110
		bars[2].Close = 90
		if direction == Long {
			bars[3].Close = 89
			bars[3].Low = 89
			bars[4].Close = 111
			bars[4].High = 111
		} else {
			bars[3].Close = 111
			bars[3].High = 111
			bars[4].Close = 89
			bars[4].Low = 89
		}
		r, err := FirstBreakout(context.Background(), "2026-06-30", opening, bars)
		if err != nil || r.Status != "candidate" || r.Direction != direction || !r.CandidateAt.Equal(marketdata.Clock("2026-06-30", 9, 9)) || !r.SignalAt.Equal(marketdata.Clock("2026-06-30", 9, 10)) || !r.EntryAt.Equal(*r.SignalAt) {
			t.Fatal("wrong breakout", err)
		}
		bars[5].Close = 1
		bars[5].Low = 1
		again, err := FirstBreakout(context.Background(), "2026-06-30", opening, bars)
		if err != nil || !reflect.DeepEqual(r, again) {
			t.Fatal("post-signal price changed entry decision")
		}
	}
}
func TestFirstBreakPreservesMissingAndFixedDeadline(t *testing.T) {
	opening := Range{100, 110, 90, 105}
	bars := entryBars()
	bars[84].Close = 111
	bars[84].High = 111
	r, err := FirstBreakout(context.Background(), "2026-06-30", opening, bars)
	if err != nil || r.Status != "candidate" || !r.EntryAt.Equal(marketdata.Clock("2026-06-30", 10, 30)) {
		t.Fatal("last entry changed", err)
	}
	r, err = FirstBreakout(context.Background(), "2026-06-30", opening, bars[1:])
	if err != nil || r.Status != "unevaluable" || r.CandidateAt != nil {
		t.Fatal("missing pre-signal bar skipped")
	}
	bars = entryBars()
	bars[85].High = 200
	bars[85].Close = 200
	r, err = FirstBreakout(context.Background(), "2026-06-30", opening, bars)
	if err != nil || r.Status != "no_trade" {
		t.Fatal("signal deadline extended")
	}
	opening.Close = opening.Open
	r, err = FirstBreakout(context.Background(), "2026-06-30", opening, nil)
	if err != nil || r.Status != "no_trade" || r.Reason != "opening_doji" {
		t.Fatal("doji entered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FirstBreakout(ctx, "2026-06-30", opening, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := FirstBreakout(context.Background(), "2026-06-31", opening, nil); err == nil {
		t.Fatal("invalid date admitted")
	}
}
