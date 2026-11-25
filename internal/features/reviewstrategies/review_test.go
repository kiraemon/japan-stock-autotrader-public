package reviewstrategies

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

func fixture() ([]string, []string, []Day) {
	dates, symbols := []string{"2026-10-15", "2026-10-16"}, []string{"0000@1", "0001@1"}
	days := []Day{}
	for _, date := range dates {
		for _, symbol := range symbols {
			for _, variant := range []r001.Variant{r001.A, r001.B} {
				for _, direction := range []r001.Direction{r001.Long, r001.Short} {
					days = append(days, Day{Symbol: symbol, Date: date, Variant: variant, Direction: direction, Status: "no_signal", Baseline: &Trade{100, 101}})
				}
			}
		}
	}
	return dates, symbols, days
}

func group(t *testing.T, groups []Group, v r001.Variant, d r001.Direction) Group {
	t.Helper()
	for _, g := range groups {
		if g.Variant == v && g.Direction == d && g.CostBPS == 10 {
			return g
		}
	}
	t.Fatal("group missing")
	return Group{}
}

func TestFixedGridAccountingNoTradeZeroMissingExcludedAndConcentration(t *testing.T) {
	dates, symbols, days := fixture()
	for i := range days {
		d := &days[i]
		if d.Date == dates[0] && d.Symbol == symbols[1] {
			d.Status = "unevaluable"
			d.Baseline = nil
			continue
		}
		if d.Direction != r001.Long {
			continue
		}
		if d.Symbol == symbols[0] && d.Date == dates[0] {
			if d.Variant == r001.B {
				d.Status = "filtered"
			} else {
				d.Status = "trade"
				d.Trade = &Trade{100, 99}
			}
		}
		if d.Symbol == symbols[1] && d.Date == dates[1] {
			d.Status = "trade"
			d.Trade = &Trade{100, 103}
		}
	}
	r, err := Review(context.Background(), dates, symbols, days)
	if err != nil {
		t.Fatal(err)
	}
	a := group(t, r.Groups, r001.A, r001.Long)
	if a.Cases != 4 || a.Eligible != 3 || a.Unevaluable != 1 || a.Trades != 2 || a.Winners != 1 || a.NoSignal != 1 || *a.MeanTrade != "90.000000" || *a.MedianTrade != "90.000000" || *a.MeanSlot != "60.000000" || *a.BaselineMean != "90.000000" || *a.PairedDelta != "0.000000" {
		t.Fatalf("bad accounting: %+v", a)
	}
	b := group(t, r.Groups, r001.B, r001.Long)
	if b.Trades != 1 || b.Filtered != 1 || *b.MeanTrade != "290.000000" || *b.MeanSlot != "96.666667" {
		t.Fatalf("bad filtered accounting: %+v", b)
	}
	s := group(t, r.Groups, r001.A, r001.Short)
	if s.Trades != 0 || s.MeanTrade != nil || s.MedianTrade != nil || *s.MeanSlot != "0.000000" || *s.BaselineMean != "-110.000000" || s.PairedDelta != nil {
		t.Fatal("zero trade treated as success or charged friction")
	}
	found := false
	for _, e := range r.Exclusions {
		if e.Kind == "symbol" && e.Value == symbols[1] {
			g := group(t, e.Groups, r001.A, r001.Long)
			if g.Trades != 1 || *g.MeanTrade != "-110.000000" {
				t.Fatal("winning symbol exclusion not retained")
			}
			found = true
		}
	}
	if !found || r.StrategyAdopted || r.TradingEligible || r.PortfolioMode || r.UnseenHoldout {
		t.Fatal("scope promoted or sensitivity missing")
	}
	// Reordering input does not alter output; caller slices are not sorted in place.
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}
	again, err := Review(context.Background(), []string{dates[1], dates[0]}, []string{symbols[1], symbols[0]}, days)
	aRaw, _ := json.Marshal(r)
	bRaw, _ := json.Marshal(again)
	if err != nil || string(aRaw) != string(bRaw) {
		t.Fatal("input order changed output")
	}
}

func TestMalformedOrIncompleteCasesFailClosed(t *testing.T) {
	mutations := []func(*[]Day){
		func(d *[]Day) { *d = (*d)[1:] },
		func(d *[]Day) { (*d)[1] = (*d)[0] },
		func(d *[]Day) { (*d)[0].Status = "winning" },
		func(d *[]Day) { (*d)[0].Baseline = nil },
		func(d *[]Day) { (*d)[0].Trade = &Trade{100, 101} },
		func(d *[]Day) { (*d)[0].Status = "trade"; (*d)[0].Trade = &Trade{0, 1} },
		func(d *[]Day) { (*d)[0].Status = "unevaluable" },
		func(d *[]Day) { (*d)[0].Date = "2026-10-17" },
	}
	for i, mutate := range mutations {
		dates, symbols, days := fixture()
		mutate(&days)
		if _, err := Review(context.Background(), dates, symbols, days); err == nil {
			t.Fatalf("mutation %d passed", i)
		}
	}
	dates, symbols, days := fixture()
	if _, err := Review(context.Background(), []string{dates[0], dates[0]}, symbols, days); err == nil {
		t.Fatal("duplicate dates")
	}
	if _, err := Review(context.Background(), dates, []string{symbols[0], symbols[0]}, days); err == nil {
		t.Fatal("duplicate symbols")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Review(ctx, dates, symbols, days); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestAllMissingAndLargeIntegerArithmetic(t *testing.T) {
	dates, symbols, days := fixture()
	for i := range days {
		days[i].Status = "unevaluable"
		days[i].Baseline = nil
	}
	r, err := Review(context.Background(), dates, symbols, days)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range r.Groups {
		if g.Eligible != 0 || g.Unevaluable != 4 || g.MeanSlot != nil || g.BaselineMean != nil {
			t.Fatal("missing days dropped or assigned returns")
		}
	}
	// Values whose intermediate diff*10000 would overflow int64 stay exact.
	for i := range days {
		days[i].Status = "trade"
		days[i].Trade = &Trade{1, math.MaxInt64}
		days[i].Baseline = &Trade{1, 1}
	}
	r, err = Review(context.Background(), dates, symbols, days)
	if err != nil {
		t.Fatal(err)
	}
	g := group(t, r.Groups, r001.A, r001.Long)
	if *g.MeanTrade != "92233720368547758059990.000000" {
		t.Fatal("integer arithmetic overflow")
	}
}
