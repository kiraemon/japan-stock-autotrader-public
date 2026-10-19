package backtests01

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

func TestRound2P0KeepsLegacyTradesAndChangesOnlyExplicitPolicyIdentity(t *testing.T) {
	for _, short := range []bool{false, true} {
		d, daily, opening := jqFixture(short)
		before, _ := json.Marshal(d)
		legacy, err := DiagnoseJQuants(context.Background(), d, daily.Date, daily, opening, 100, "3/2")
		if err != nil {
			t.Fatal(err)
		}
		r, err := DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "3/2", "R2-P0")
		if err != nil || r.Status != legacy.Status || !reflect.DeepEqual(r.Trade, legacy.Trade) || !reflect.DeepEqual(r.Decision, legacy.Decision) || !reflect.DeepEqual(r.Diagnostic, legacy.Diagnostic) {
			t.Fatal("P0 changed legacy price rules", err)
		}
		if r.Round2 == nil || r.Rule != "R2-P0" || r.Round2.ScopeSHA != s01.Round2ScopeSHA256 || r.TradingEligible || r.StrategyAdopted || r.QualityPromoted || legacy.Round2 != nil {
			t.Fatal("policy provenance or default boundary lost")
		}
		after, _ := json.Marshal(d)
		if string(before) != string(after) {
			t.Fatal("input changed")
		}
	}
}

func TestRound2P1UsesWiderExactDistanceWithUnchangedEntryAndTimeLimit(t *testing.T) {
	for _, short := range []bool{false, true} {
		d, daily, opening := jqFixture(short)
		r, err := DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "1", "R2-P1")
		if err != nil || r.Trade == nil || r.Rule != "R2-P1" {
			t.Fatal("P1 replay failed", err)
		}
		distance := r.Trade.EntryPrice - r.Trade.StopPrice
		if short {
			distance = -distance
		}
		if distance != 200 || r.Trade.ExitAt.Sub(r.Trade.EntryAt).Minutes() > 20 {
			t.Fatal("wider stop or time limit changed")
		}
	}
}

func TestRound2P2ChecksRVOLBoundaryBeforePriceReplayAndAfterQuality(t *testing.T) {
	d, daily, opening := jqFixture(false)
	r, err := DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "149/100", "R2-P2")
	if err != nil || r.Status != "no_trade" || r.Trade != nil || r.Decision != nil || r.RVOL != "149/100" || len(r.Reasons) != 1 || r.Reasons[0] != "opening_rvol_below_round2_minimum" {
		t.Fatal("RVOL filter lost original input or ran price replay", err)
	}
	r, err = DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "3/2", "R2-P2")
	if err != nil || r.Trade == nil {
		t.Fatal("inclusive RVOL boundary changed", err)
	}
	// Low RVOL cannot hide an incomplete provider price window.
	d.Days[0].Bars = d.Days[0].Bars[:len(d.Days[0].Bars)-1]
	daily = jqDaily(d)
	r, err = DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "1", "R2-P2")
	if err != nil || r.Status != "unevaluable" || r.Trade != nil || r.Diagnostic == nil || r.Diagnostic.Eligible {
		t.Fatal("RVOL filter promoted incomplete input", err)
	}
}

func TestRound2RejectsUndeclaredRuleInvalidIndicatorsAndOpeningMismatch(t *testing.T) {
	d, daily, opening := jqFixture(false)
	for _, id := range []string{"", "R2-P3", "R2-P1+P2"} {
		if _, err := DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "1", id); err == nil {
			t.Fatal("unknown rule admitted")
		}
	}
	if _, err := DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "0", "1", "R2-P0"); err == nil {
		t.Fatal("zero ATR admitted")
	}
	if _, err := DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "1/0", "R2-P0"); err == nil {
		t.Fatal("invalid RVOL admitted")
	}
	opening.Open++
	if _, err := DiagnoseRound2(context.Background(), d, daily.Date, daily, opening, "1000", "1", "R2-P0"); err == nil {
		t.Fatal("mismatched prepared opening admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DiagnoseRound2(ctx, d, daily.Date, daily, opening, "1000", "1", "R2-P0"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
