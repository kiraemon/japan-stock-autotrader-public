package main

import (
	"context"
	"math/rand/v2"
	"reflect"
	"testing"
)

func pathFixture() (entry, []bar) {
	e := entry{Date: "2025-11-04", Symbol: "0000@1", Rank: 1, Status: "executed", Trade: trade{Direction: "long", EntryAt: clock("2025-11-04", 540), ExitAt: clock("2025-11-04", 542), Entry: 10000, Exit: 9900, Reason: "intrabar_stop_touch"}}
	bars := []bar{}
	for m := 540; m <= 560; m++ {
		bars = append(bars, bar{At: clock(e.Date, m), Open: 9800, High: 9900, Low: 9700, Close: 9800})
	}
	bars[0] = bar{At: e.Trade.EntryAt, Open: 10000, High: 10000, Low: 9900, Close: 9900}
	bars[1] = bar{At: clock(e.Date, 541), Open: 9900, High: 12000, Low: 9700, Close: 9800}
	return e, bars
}
func TestRecoveryExcludesAmbiguousStopBarAndDeadlineHighLow(t *testing.T) {
	e, bars := pathFixture()
	bars[20].High = 15000
	before := append([]bar{}, bars...)
	x, err := inspectPath(e, bars, models[0])
	if err != nil || !x.Stop || x.Recovered || x.Endpoint != -200 || x.MFE != 2000 || x.EndpointDeltaJPY != -1 {
		t.Fatal("ambiguous exit-bar or post-deadline prices entered recovery", err, x)
	}
	if !reflect.DeepEqual(before, bars) {
		t.Fatal("path mutated")
	}
	bars[2].High = 10000
	x, err = inspectPath(e, bars, models[0])
	if err != nil || !x.Recovered {
		t.Fatal("known later entry touch missed")
	}
	bars[2].High = 9900
	bars[20].Open = 10000
	x, err = inspectPath(e, bars, models[0])
	if err != nil || !x.Recovered || x.Endpoint != 0 {
		t.Fatal("deadline open touch missed")
	}
}
func TestShortPathAndMissingMinuteCannotBeSkipped(t *testing.T) {
	e, bars := pathFixture()
	e.Trade.Direction = "short"
	e.Trade.Exit = 10100
	x, err := inspectPath(e, bars, models[0])
	if err != nil || x.Endpoint != 200 || x.MFE != 300 || x.MAE != -2000 {
		t.Fatal("short path sign/order incorrect", err, x)
	}
	bars = append(bars[:8], bars[9:]...)
	if _, e := inspectPath(e, bars, models[0]); e == nil {
		t.Fatal("missing path minute skipped")
	}
}
func TestBootstrapConstantPairedSeriesAndCancellation(t *testing.T) {
	s := [][]float64{{2, 2, 2, 2, 2, 2}, {2, 2, 2, 2, 2, 2}, {-1, -1, -1, -1, -1, -1}}
	for _, block := range []int{1, 5} {
		a, b, e := bootstrap(context.Background(), s, []float64{3, 3, 3}, block, 100, 99)
		if e != nil || a[0] != [2]float64{9, 9} || a[2] != [2]float64{-9, -9} || b[0] != [2]float64{0, 0} || b[1] != [2]float64{18, 18} {
			t.Fatal("constant daily cashflow/paired identities failed", e, a, b)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, e := bootstrap(ctx, s, []float64{3, 3, 3}, 5, 100, 99); e == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestCircularSamplingExactLengthAndRepeatability(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 5))
	ix := sampleIndices(r, 7, 5)
	if len(ix) != 7 {
		t.Fatal("wrong sample length")
	}
	for i, v := range ix {
		if v < 0 || v >= 7 {
			t.Fatal("index bounds")
		}
		if i%5 > 0 && v != (ix[i-1]+1)%7 {
			t.Fatal("block order not preserved")
		}
	}
	s := [][]float64{{1, 2, 3, 4, 5, 6, 7}, {1, 2, 3, 4, 5, 6, 7}, {7, 6, 5, 4, 3, 2, 1}}
	a, b, e := bootstrap(context.Background(), s, []float64{1, 1, 1}, 5, 100, 11)
	c, d, f := bootstrap(context.Background(), s, []float64{1, 1, 1}, 5, 100, 11)
	if e != nil || f != nil || !reflect.DeepEqual(a, c) || !reflect.DeepEqual(b, d) || b[0] != [2]float64{0, 0} {
		t.Fatal("paired resampling not deterministic")
	}
}
func TestFixedMorningEndpointHasNoPriceStop(t *testing.T) {
	for _, c := range []struct {
		model       string
		entry, exit int
	}{{models[1], 570, 600}, {models[2], 541, 570}} {
		e, _ := pathFixture()
		e.Trade.EntryAt = clock(e.Date, c.entry)
		e.Trade.ExitAt = clock(e.Date, c.exit)
		e.Trade.Reason = "fixed_morning_time_exit"
		bars := []bar{}
		for m := c.entry; m <= c.exit; m++ {
			bars = append(bars, bar{At: clock(e.Date, m), Open: 10000, High: 11000, Low: 9000, Close: 10000})
		}
		if c.model == models[2] {
			bars = append([]bar{{At: clock(e.Date, 540), Open: 9900, High: 9900, Low: 9900, Close: 9900}}, bars...)
		}
		bars[len(bars)-1].Open = 10100
		e.Trade.Exit = 10100
		x, err := inspectPath(e, bars, c.model)
		if err != nil || x.Stop || x.Endpoint != 100 || x.EndpointDeltaJPY != 0 {
			t.Fatal("fixed morning endpoint changed", err, x)
		}
		if c.model == models[2] && (!x.EarlyGapAvailable || x.EarlyGapJPY != 1) {
			t.Fatal("first-minute mark not separated from execution")
		}
	}
}
