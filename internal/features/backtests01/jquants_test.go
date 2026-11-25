package backtests01

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/s01"
)

func jqFixture(short bool) (marketdata.Dataset, marketdata.DailyBar, s01.Range) {
	d, opening := fixture(short)
	d.Origin = "observed"
	d.Environment = "production"
	d.Source.Format = marketdata.JQuantsJoinedFormat
	d.Days[0].Issues = []string{"coverage_unverified", "corporate_actions_unverified"}
	bars := []marketdata.Bar{}
	for m := 0; m < 5; m++ {
		v := int64(100)
		bars = append(bars, marketdata.Bar{Start: marketdata.Clock("2026-06-30", 9, m), Open: opening.Open, High: opening.High, Low: opening.Low, Close: opening.Close, Volume: &v})
	}
	d.Days[0].Bars = append(bars, d.Days[0].Bars...)
	return d, jqDaily(d), opening
}
func jqDaily(d marketdata.Dataset) marketdata.DailyBar {
	day := d.Days[0]
	bars := day.Bars
	r := marketdata.DailyBar{Date: day.Date, Code: "00000", HasTrades: true, Open: bars[0].Open, High: bars[0].High, Low: bars[0].Low, Close: bars[len(bars)-1].Close, AdjustmentFactor: "1"}
	for _, b := range bars {
		r.Volume += *b.Volume
		if b.High > r.High {
			r.High = b.High
		}
		if b.Low < r.Low {
			r.Low = b.Low
		}
	}
	return r
}
func TestJQuantsPriceAdapterGatesRVOLAndKeepsSourceQuality(t *testing.T) {
	for _, short := range []bool{false, true} {
		d, daily, o := jqFixture(short)
		before, _ := json.Marshal(d)
		r, err := DiagnoseJQuants(context.Background(), d, daily.Date, daily, o, 100, "1")
		after, _ := json.Marshal(d)
		if err != nil || r.Status != "traded" || r.Trade == nil || !r.Diagnostic.Eligible || len(r.Diagnostic.OriginalIssues) != 2 || r.QualityPromoted || r.TradingEligible || r.StrategyAdopted || r.RVOL != "1" || string(before) != string(after) {
			t.Fatal("provider diagnostic contract changed", err)
		}
		r, err = DiagnoseJQuants(context.Background(), d, daily.Date, daily, o, 100, "99/100")
		if err != nil || r.Status != "no_trade" || r.Trade != nil || r.Decision != nil || r.Reasons[0] != "opening_rvol_below_one" {
			t.Fatal("low RVOL traded", err)
		}
		if _, err := DiagnoseJQuants(context.Background(), d, daily.Date, daily, o, 100, "private-marker"); err == nil {
			t.Fatal("bad RVOL admitted")
		}
		badOpening := o
		badOpening.High++
		if _, err := DiagnoseJQuants(context.Background(), d, daily.Date, daily, badOpening, 100, "99/100"); err == nil {
			t.Fatal("incorrect prepared opening admitted")
		}
		normal, err := Run(context.Background(), d, daily.Date, o, 100)
		if err != nil || normal.Status != "unevaluable" || normal.Trade != nil {
			t.Fatal("normal observed gate changed")
		}
	}
}
func TestJQuantsPriceAdapterAllowsOnlyOpeningPrefixAndRejectsLateMissing(t *testing.T) {
	d, _, o := jqFixture(false)
	d.Days[0].Bars = d.Days[0].Bars[3:]
	daily := jqDaily(d)
	r, err := DiagnoseJQuants(context.Background(), d, daily.Date, daily, o, 100, "1")
	if err != nil || r.Status != "traded" || len(r.Diagnostic.LeadingAssumed) != 3 {
		t.Fatal("explicit prefix policy changed", err)
	}
	// Even a stop in the entry minute does not excuse a missing 10:50 bar.
	d, _, o = jqFixture(false)
	d.Days[0].Bars[6].Low = 1020
	d.Days[0].Bars = d.Days[0].Bars[:110]
	daily = jqDaily(d)
	r, err = DiagnoseJQuants(context.Background(), d, daily.Date, daily, o, 100, "1")
	if err != nil || r.Status != "unevaluable" || r.Trade != nil || r.Diagnostic.Eligible {
		t.Fatal("early stop admitted incomplete fixed window", err)
	}
}
