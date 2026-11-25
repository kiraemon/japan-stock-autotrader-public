package backtests01

import (
	"context"
	"encoding/json"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/s01"
	"strings"
	"testing"
	"time"
)

func fixture(short bool) (marketdata.Dataset, s01.Range) {
	opening := s01.Range{Open: 1000, High: 1100, Low: 900, Close: 1050}
	if short {
		opening.Close = 950
	}
	d := marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: "0000@1", Environment: "test", Origin: "synthetic", Converter: "test", Source: marketdata.Source{Format: "synthetic", Label: "start", SHA256: strings.Repeat("a", 64), ReceivedAt: marketdata.Clock("2026-06-30", 16, 0)}}
	day := marketdata.Day{Date: "2026-06-30"}
	for at := marketdata.Clock(day.Date, 9, 5); !at.After(marketdata.Clock(day.Date, 10, 50)); at = at.Add(time.Minute) {
		v := int64(100)
		b := marketdata.Bar{Start: at, Open: 1120, High: 1130, Low: 1110, Close: 1120, Volume: &v}
		if short {
			b.Open = 880
			b.High = 890
			b.Low = 870
			b.Close = 880
		}
		day.Bars = append(day.Bars, b)
	}
	d.Days = []marketdata.Day{day}
	return d, opening
}
func TestStopGapDeadlineAndIntrabarCapitalOrder(t *testing.T) {
	for _, short := range []bool{false, true} {
		for _, mode := range []string{"entry_stop", "later_stop", "gap", "time"} {
			d, o := fixture(short)
			bars := d.Days[0].Bars
			expectedExit := int64(1020)
			if short {
				expectedExit = 980
			}
			at := marketdata.Clock("2026-06-30", 9, 7)
			barStart := at
			reason := "intrabar_stop_touch"
			precision := "intrabar_unknown_capital_released_at_bar_end"
			switch mode {
			case "entry_stop":
				if short {
					bars[1].High = 980
				} else {
					bars[1].Low = 1020
				}
				barStart = marketdata.Clock("2026-06-30", 9, 6)
			case "later_stop":
				if short {
					bars[2].High = 980
				} else {
					bars[2].Low = 1020
				}
				at = at.Add(time.Minute)
			case "gap":
				if short {
					bars[2].Open = 1010
					bars[2].High = 1020
					expectedExit = 1010
				} else {
					bars[2].Open = 990
					bars[2].Low = 980
					expectedExit = 990
				}
				reason = "adverse_gap_through_stop"
				precision = "minute_open_model"
			case "time":
				at = marketdata.Clock("2026-06-30", 9, 26)
				barStart = at
				reason = "maximum_hold_20m"
				precision = "minute_open_model"
				if short {
					bars[21].High = 2000
					expectedExit = 880
				} else {
					bars[21].Low = 1
					expectedExit = 1120
				}
			}
			before, _ := json.Marshal(d)
			r, err := Run(context.Background(), d, "2026-06-30", o, 100)
			after, _ := json.Marshal(d)
			if err != nil || r.Status != "traded" || r.Trade == nil || r.QualityPromoted || r.TradingEligible || r.StrategyAdopted || string(before) != string(after) {
				t.Fatal("replay contract changed", mode, err)
			}
			tr := r.Trade
			if tr.ExitPrice != expectedExit || tr.Reason != reason || !tr.ExitAt.Equal(at) || !tr.ExitBarStart.Equal(barStart) || tr.ExitTimePrecision != precision || !tr.EntryAt.Equal(marketdata.Clock("2026-06-30", 9, 6)) {
				t.Fatal("stop/open/deadline order changed", mode)
			}
			// Entry is the next open (1120/880), never the breakout boundary.
			if (!short && tr.EntryPrice != 1120) || (short && tr.EntryPrice != 880) {
				t.Fatal("retrospective boundary fill")
			}
		}
	}
}
func TestMissingBarsAndObservedQualityCannotBePromoted(t *testing.T) {
	for _, fault := range []string{"entry", "holding", "deadline", "observed", "day_issue", "bar_issue"} {
		d, o := fixture(false)
		switch fault {
		case "entry":
			d.Days[0].Bars = append(d.Days[0].Bars[:1], d.Days[0].Bars[2:]...)
		case "holding":
			d.Days[0].Bars = append(d.Days[0].Bars[:2], d.Days[0].Bars[3:]...)
		case "deadline":
			d.Days[0].Bars = append(d.Days[0].Bars[:21], d.Days[0].Bars[22:]...)
		case "observed":
			d.Origin = "observed"
			d.Source.Format = "jquants-bulk-minute-v0.1"
		case "day_issue":
			d.Days[0].Issues = []string{"coverage_unverified"}
		case "bar_issue":
			d.Days[0].Bars[1].Issues = []string{"invalid"}
		}
		r, err := Run(context.Background(), d, "2026-06-30", o, 100)
		if err != nil || r.Status != "unevaluable" || r.Trade != nil || r.QualityPromoted || r.TradingEligible || len(r.Reasons) == 0 {
			t.Fatal("unverified/missing input traded", fault, err)
		}
	}
	d, o := fixture(false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, d, "2026-06-30", o, 100); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := Run(context.Background(), d, "2026-06-30", o, 0); err == nil {
		t.Fatal("zero stop admitted")
	}
}
