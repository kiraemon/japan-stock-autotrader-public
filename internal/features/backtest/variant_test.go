package backtest

import (
	"context"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func TestVariantBThresholdAndNoSecondCandidate(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		d := Synthetic(side)
		v := int64(150)
		d.Days[0].Bars[15].Volume = &v
		r, err := RunVariant(context.Background(), d, side, r001.B)
		if err != nil || r.Days[0].Trade == nil || !r.Days[0].Filter.Passed || r.Rule != r001.VersionFor(side, r001.B) {
			t.Fatalf("threshold rejected: %+v %v", r, err)
		}
		v = 149
		r, err = RunVariant(context.Background(), d, side, r001.B)
		if err != nil || r.Days[0].Status != "filtered" || r.Days[0].CandidateAt == nil || r.Days[0].Trade != nil || r.Costs[0].Trades != 0 || r.BaselineCosts[0].Trades != 1 {
			t.Fatalf("filter result lost: %+v %v", r, err)
		}
		// A remains unchanged by a B-only condition.
		if a := runFixture(t, d, side); a.Days[0].Trade == nil || a.Days[0].Filter != nil {
			t.Fatal("A changed")
		}
		// Later stronger breakout must not replace the first rejected signal.
		d.Days[0].Bars[16].Close = 1000000
		if side == r001.Long {
			d.Days[0].Bars[16].Low = 1000000
		} else {
			d.Days[0].Bars[16].High = 1000000
		}
		large := int64(99999)
		d.Days[0].Bars[17].Volume = &large
		r, err = RunVariant(context.Background(), d, side, r001.B)
		if err != nil || r.Days[0].Status != "filtered" || r.Days[0].CandidateAt.Minute() != 16 {
			t.Fatal("later signal selected")
		}
		d.Days[0].Bars[14].Volume = nil
		r, err = RunVariant(context.Background(), d, side, r001.B)
		if err != nil || r.Days[0].Status != "unevaluable" || r.Days[0].Reasons[0] != "volume_input_unavailable" {
			t.Fatal("unknown volume silently filtered/passed")
		}
	}
}
