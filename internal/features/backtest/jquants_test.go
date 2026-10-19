package backtest

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func jqFixture(side r001.Direction) (marketdata.Dataset, marketdata.DailyBar) {
	d := Synthetic(side)
	d.Origin = "observed"
	d.Environment = "production"
	d.Source.Format = "jquants-bulk-minute-v0.1"
	d.Days[0].Issues = []string{"coverage_unverified", "corporate_actions_unverified"}
	return d, jqDaily(d)
}
func jqDaily(d marketdata.Dataset) marketdata.DailyBar {
	day := d.Days[0]
	bars := day.Bars
	r := marketdata.DailyBar{Date: day.Date, Code: "00000", HasTrades: true, Open: bars[0].Open, High: bars[0].High, Low: bars[0].Low, Close: bars[len(bars)-1].Close, AdjustmentFactor: "1"}
	r.Code = d.Symbol[:4] + "0"
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
func TestJQuantsAdapterKeepsAllOriginalABDirectionsAndQuality(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		for _, variant := range []r001.Variant{r001.A, r001.B} {
			d, daily := jqFixture(side)
			before, _ := json.Marshal(d)
			want, err := RunVariant(context.Background(), Synthetic(side), side, variant)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DiagnoseJQuants(context.Background(), d, daily.Date, daily, side, variant)
			after, _ := json.Marshal(d)
			if err != nil || !reflect.DeepEqual(want.Days, got.Days) || !got.JQuantsDiagnostic.Eligible || !got.JQuantsDiagnostic.DailyMatch || got.JQuantsDiagnostic.Observed != 111 || len(got.JQuantsDiagnostic.OriginalIssues) != 2 || got.StrategyAdopted || got.PortfolioMode || got.ShortAvailabilityVerified || string(before) != string(after) {
				t.Fatal("adapter changed original A/B rule or quality", err)
			}
			normal, err := RunVariant(context.Background(), d, side, variant)
			if err != nil || normal.Days[0].Trade != nil || normal.Days[0].Status != "unevaluable" {
				t.Fatal("normal gate changed")
			}
		}
	}
}
func TestJQuantsAdapterKeepsPrefixMissingForStrictR001AndFullWindow(t *testing.T) {
	d, _ := jqFixture(r001.Long)
	d.Days[0].Bars = d.Days[0].Bars[3:]
	daily := jqDaily(d)
	r, err := DiagnoseJQuants(context.Background(), d, daily.Date, daily, r001.Long, r001.A)
	if err != nil || !r.JQuantsDiagnostic.Eligible || r.Days[0].Status != "unevaluable" || r.Days[0].Trade != nil || len(r.JQuantsDiagnostic.LeadingAssumed) != 3 {
		t.Fatal("R001 invented leading bars", err)
	}
	d, _ = jqFixture(r001.Long)
	d.Days[0].Bars = d.Days[0].Bars[:110]
	daily = jqDaily(d)
	r, err = DiagnoseJQuants(context.Background(), d, daily.Date, daily, r001.Long, r001.A)
	if err != nil || r.JQuantsDiagnostic.Eligible || r.Days[0].Status != "unevaluable" || r.Days[0].Trade != nil {
		t.Fatal("early result admitted incomplete window", err)
	}
}
