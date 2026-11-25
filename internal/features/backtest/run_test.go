package backtest

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

func runFixture(t *testing.T, d marketdata.Dataset, side r001.Direction) Report {
	t.Helper()
	r, err := Run(context.Background(), d, side)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestLongShortExecutionAndRoundTripCosts(t *testing.T) {
	for _, tc := range []struct {
		side       r001.Direction
		gross, net string
	}{{r001.Long, "98.039216", "78.039216"}, {r001.Short, "102.040816", "82.040816"}} {
		t.Run(string(tc.side), func(t *testing.T) {
			d := Synthetic(tc.side)
			// Saved and reloaded time zones must match the strategy clock by instant.
			raw, _ := json.Marshal(d)
			var decoded marketdata.Dataset
			if json.Unmarshal(raw, &decoded) != nil {
				t.Fatal("fixture decoding")
			}
			r := runFixture(t, decoded, tc.side)
			tr := r.Days[0].Trade
			if tr == nil || r.Scope != "synthetic_logic_test" || r.StrategyAdopted || r.PortfolioMode || r.ShortAvailabilityVerified {
				t.Fatal("research scope violated")
			}
			if tr.EntryAt.In(marketdata.JST).Format("15:04") != "09:16" || tr.ExitAt.In(marketdata.JST).Format("15:04") != "09:36" || tr.Reason != "maximum_hold" || tr.GrossBPS != tc.gross {
				t.Fatalf("trade=%+v", tr)
			}
			if r.Costs[3].RoundTripBPS != 20 || *r.Costs[3].MeanBPS != tc.net || r.Costs[3].Trades != 1 {
				t.Fatalf("cost=%+v", r.Costs[3])
			}
			if !reflect.DeepEqual(r, runFixture(t, decoded, tc.side)) {
				t.Fatal("same input changed results")
			}
		})
	}
}
func TestRangeReversalAndMissingExitAreNotDropped(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		d := Synthetic(side)
		b := &d.Days[0].Bars[18]
		next := &d.Days[0].Bars[19]
		if side == r001.Long {
			b.Close = 1000000
			b.Low = 1000000
			next.Open = 990000
			next.Low = 990000
		} else {
			b.Close = 1000000
			b.High = 1000000
			next.Open = 1010000
			next.High = 1010000
		}
		r := runFixture(t, d, side)
		if r.Days[0].Trade == nil || r.Days[0].Trade.Reason != "range_reversal" || r.Days[0].Trade.ExitAt.In(marketdata.JST).Minute() != 19 || r.Costs[0].Winners != 0 {
			t.Fatal("reversal/short loss sign failed")
		}
		d.Days[0].Bars = append(d.Days[0].Bars[:19], d.Days[0].Bars[20:]...)
		r = runFixture(t, d, side)
		if r.Days[0].Status != "unevaluable" || r.Days[0].Trade != nil || r.Days[0].CandidateAt == nil || r.Costs[0].Trades != 0 {
			t.Fatal("missing losing exit silently removed")
		}
	}
}
func TestNoSignalEqualityAndLastAllowedSignal(t *testing.T) {
	d := Synthetic(r001.Long)
	for i := 15; i < len(d.Days[0].Bars); i++ {
		b := &d.Days[0].Bars[i]
		b.Open = 1010000
		b.High = 1010000
		b.Low = 1010000
		b.Close = 1010000
	}
	if r := runFixture(t, d, r001.Long); r.Days[0].Status != "no_signal" || r.Costs[0].MeanBPS != nil {
		t.Fatal("equality is not a strict breakout")
	}
	d.Days[0].Bars[89].Close = 1020000
	d.Days[0].Bars[89].High = 1020000
	if r := runFixture(t, d, r001.Long); r.Days[0].Trade == nil || r.Days[0].Trade.EntryAt.In(marketdata.JST).Format("15:04") != "10:30" || r.Days[0].Trade.ExitAt.In(marketdata.JST).Format("15:04") != "10:50" {
		t.Fatalf("last signal=%+v", r.Days[0])
	}
	d.Days[0].Bars[89].Close = 1010000
	d.Days[0].Bars[90].Close = 1020000
	d.Days[0].Bars[90].High = 1020000
	if r := runFixture(t, d, r001.Long); r.Days[0].Trade != nil {
		t.Fatal("10:30-start signal must be outside window")
	}
}
func TestTimeExitCollisionAndFutureBarsDoNotChangePastTrade(t *testing.T) {
	d := Synthetic(r001.Long)
	d.Days[0].Bars[35].Close = 1000000
	d.Days[0].Bars[35].Low = 1000000
	r := runFixture(t, d, r001.Long)
	if r.Days[0].Trade.Reason != "maximum_hold" {
		t.Fatal("same-time exit counted twice")
	}
	before := *r.Days[0].Trade
	for i := 37; i < len(d.Days[0].Bars); i++ {
		b := &d.Days[0].Bars[i]
		b.Open = 9990000
		b.Close = 9990000
		b.High = 9990000
		b.Low = 9990000
	}
	after := runFixture(t, d, r001.Long).Days[0].Trade
	if !reflect.DeepEqual(before, *after) {
		t.Fatal("future bars changed an earlier trade")
	}
}
func TestMissingQualityAndBadInputsFailClosed(t *testing.T) {
	d := Synthetic(r001.Long)
	d.Origin = "observed"
	d.Days[0].Issues = nil
	r := runFixture(t, d, r001.Long)
	if r.Days[0].Status != "unevaluable" || r.Days[0].Trade != nil {
		t.Fatal("missing real-data quality treated as verified")
	}
	d = Synthetic(r001.Long)
	d.Days[0].Bars = append(d.Days[0].Bars[:5], d.Days[0].Bars[6:]...)
	if r := runFixture(t, d, r001.Long); r.Days[0].Status != "unevaluable" {
		t.Fatal("initial-range hole ignored")
	}
	d = Synthetic(r001.Long)
	d.Days[0].Bars[20].Issues = []string{"close_order_unverified"}
	if r := runFixture(t, d, r001.Long); r.Days[0].Status != "unevaluable" {
		t.Fatal("uncertain exit path used")
	}
	d = Synthetic(r001.Long)
	d.Days[0].Bars[1].Start = d.Days[0].Bars[0].Start
	if _, err := Run(context.Background(), d, r001.Long); err == nil {
		t.Fatal("duplicate bars accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, Synthetic(r001.Long), r001.Long); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := Run(context.Background(), Synthetic(r001.Long), "wrong"); err == nil {
		t.Fatal("bad direction accepted")
	}
}
