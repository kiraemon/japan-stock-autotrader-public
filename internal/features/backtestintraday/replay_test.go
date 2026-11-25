package backtestintraday

import (
	"context"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/round3"
)

// All values are fabricated; observed labels exercise guarded provider paths.
func fixture() (marketdata.Dataset, []string, []marketdata.DailyBar, string) {
	date := "2026-06-30"
	previous := []string{}
	for at := marketdata.Clock("2026-06-10", 0, 0); marketdata.Date(at) < date; at = at.AddDate(0, 0, 1) {
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			previous = append(previous, marketdata.Date(at))
		}
	}
	d := marketdata.Dataset{Version: 1, PriceScale: 10000, Symbol: "0000@1", Origin: "observed", Environment: "production", Converter: "fabricated-test", Source: marketdata.Source{Format: marketdata.JQuantsTurnoverJoinedFormat, SHA256: strings.Repeat("a", 64), Label: "start", ReceivedAt: marketdata.Clock(date, 16, 0)}}
	refs := []marketdata.DailyBar{}
	for _, day := range append(append([]string{}, previous...), date) {
		row := marketdata.Day{Date: day, Issues: []string{"coverage_unverified"}}
		for i := 0; i < 111; i++ {
			v := int64(100)
			b := marketdata.Bar{Start: marketdata.Clock(day, 9, 0).Add(time.Duration(i) * time.Minute), Open: 1000000, High: 1060000, Low: 950000, Close: 1000000, Volume: &v, TurnoverJPY: "10000"}
			row.Bars = append(row.Bars, b)
		}
		if day == date {
			row.Bars[29].Close = 1030000
			row.Bars[30].Open = 1040000
			row.Bars[60].Open = 1050000
		}
		d.Days = append(d.Days, row)
		refs = append(refs, marketdata.DailyBar{Date: day, Code: "00000", HasTrades: true, Open: 1000000, High: 1060000, Low: 950000, Close: 1000000, Volume: 11100, Turnover: "1110000", AdjustmentFactor: "1"})
	}
	return d, previous, refs, date
}
func TestFixedMorningReplayUsesNextOpenAndOriginalAmount(t *testing.T) {
	d, previous, refs, date := fixture()
	before, _ := json.Marshal(d)
	r, err := Diagnose(context.Background(), d, date, previous, refs, round3.NoiseRule)
	if err != nil || r.Status != "traded" || r.Trade == nil || r.Trade.EntryPrice != 1040000 || r.Trade.ExitPrice != 1050000 || r.Noise.VWAP != "100" || len(r.History) != 14 || r.Trade.GrossBPS != big.NewRat(10000, 104).RatString() || r.Quality || r.Trading {
		t.Fatal("NA replay or quality", err)
	}
	after, _ := json.Marshal(d)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("input mutated")
	}
	current := &d.Days[len(d.Days)-1]
	current.Bars[0].Open = 1030000
	current.Bars[1].Open = 1020000
	refs[len(refs)-1].Open = 1030000
	gap, err := Diagnose(context.Background(), d, date, previous, refs, round3.GapRule)
	if err != nil || gap.Status != "traded" || gap.Trade.Direction != "short" || gap.Trade.EntryPrice != 1020000 || gap.Trade.ExitPrice != 1040000 || !gap.Trade.EntryAt.Equal(marketdata.Clock(date, 9, 1)) {
		t.Fatal("gap entry used auction price", err)
	}
	current.Bars[1].Open = 1010000
	changed, err := Diagnose(context.Background(), d, date, previous, refs, round3.GapRule)
	if err != nil || !reflect.DeepEqual(gap.Gap, changed.Gap) || changed.Trade.EntryPrice == gap.Trade.EntryPrice {
		t.Fatal("future entry price leaked into decision", err)
	}
}
func TestMissingQualityAndCorporateActionsAreNotPromoted(t *testing.T) {
	for _, fault := range []string{"target_gap", "history_close", "history_amount", "current_action", "unknown_issue", "unfinished"} {
		d, previous, refs, date := fixture()
		switch fault {
		case "target_gap":
			d.Days[14].Bars = append(d.Days[14].Bars[:40], d.Days[14].Bars[41:]...)
			refs[14].Volume -= 100
			refs[14].Turnover = "1100000"
		case "history_close":
			d.Days[0].Bars = append(d.Days[0].Bars[:29], d.Days[0].Bars[30:]...)
			refs[0].Volume -= 100
			refs[0].Turnover = "1100000"
		case "history_amount":
			refs[0].Turnover = "1110001"
		case "current_action":
			refs[14].AdjustmentFactor = "1/2"
		case "unknown_issue":
			d.Days[14].Issues = []string{"unknown"}
		case "unfinished":
			d.Source.ReceivedAt = marketdata.Clock(date, 9, 30)
		}
		r, err := Diagnose(context.Background(), d, date, previous, refs, round3.NoiseRule)
		if err != nil || r.Status != "unevaluable" || r.Trade != nil || len(r.Reasons) == 0 || r.Quality || r.Trading {
			t.Fatal("unavailable input promoted", fault, err)
		}
	}
	d, previous, refs, date := fixture()
	refs[0].AdjustmentFactor = "1/2"
	r, err := Diagnose(context.Background(), d, date, previous, refs, round3.NoiseRule)
	if err != nil || r.Status != "traded" {
		t.Fatal("same-day historical raw ratio incorrectly adjusted", err)
	}
	d.Source.Format = marketdata.JQuantsJoinedFormat
	if _, err := Diagnose(context.Background(), d, date, previous, refs, round3.NoiseRule); err == nil {
		t.Fatal("missing turnover source accepted")
	}
}
