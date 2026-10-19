package round4

import (
	"context"
	"fmt"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"reflect"
	"testing"
	"time"
)

func TestSelectCausalDenominatorTiesAndInputOrder(t *testing.T) {
	input := []Opening{}
	for i := 1; i <= 30; i++ {
		input = append(input, Opening{Symbol: fmt.Sprintf("%04d@1", i), LiquidityRank: i, Available: true, RVOL: "2"})
	}
	input[0].Available = false
	input[0].RVOL = ""
	input[1].RVOL = "1/2"
	input[29].RVOL = "3"
	before := append([]Opening{}, input...)
	p, e := Select(context.Background(), input)
	if e != nil {
		t.Fatal(e)
	}
	n := 0
	for _, v := range p {
		if v.Selected {
			n++
		}
	}
	if n != 20 || p[0].Selected || p[1].Selected || !p[29].Selected || p[29].RVOLRank != 1 || p[2].RVOLRank != 2 || p[21].Selected {
		t.Fatal("rank, missing, threshold or tie")
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("mutated input")
	}
	input[0], input[29] = input[29], input[0]
	q, e := Select(context.Background(), input)
	if e != nil || q[0].RVOLRank != 1 || q[29].Selected {
		t.Fatal("order dependency")
	}
	input[0].Symbol = input[1].Symbol
	if _, e := Select(context.Background(), input); e == nil {
		t.Fatal("duplicate admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Select(ctx, before); e == nil {
		t.Fatal("cancel ignored")
	}
}

func exitDay() marketdata.Day {
	d := marketdata.Day{Date: "2025-11-04"}
	v := int64(1)
	for i := 0; i <= 60; i++ {
		d.Bars = append(d.Bars, marketdata.Bar{Start: marketdata.Clock(d.Date, 9, 0).Add(time.Duration(i) * time.Minute), Open: 1000000, Close: 1000000, Low: 900000, High: 1100000, Volume: &v, TurnoverJPY: "100"})
	}
	return d
}
func TestVWAPExitNextOpenEqualityBothDirectionsAndNoFutureLeak(t *testing.T) {
	for _, direction := range []string{"long", "short"} {
		d := exitDay()
		d.Bars[31].Open = 1050000
		got, e := VWAPExit(context.Background(), d, direction)
		if e != nil || !got.At.Equal(marketdata.Clock(d.Date, 9, 31)) || got.Price != 1050000 || got.VWAP != "100" {
			t.Fatal(direction, got, e)
		}
		// Future bar close cannot affect the earlier choice; the fixed-window
		// source gate is deliberately a caller responsibility, not this rule.
		d.Bars[31].Close = 900000
		d.Bars[32].Volume = nil
		other, e := VWAPExit(context.Background(), d, direction)
		if e != nil || !reflect.DeepEqual(got, other) {
			t.Fatal("future input leaked", e)
		}
	}
	d := exitDay()
	d.Bars = append(d.Bars[:31], d.Bars[32:]...)
	if _, e := VWAPExit(context.Background(), d, "long"); e == nil {
		t.Fatal("missing following open admitted")
	}
}
func TestVWAPNoCrossDeadlineMissingAndPrefix(t *testing.T) {
	for _, direction := range []string{"long", "short"} {
		d := exitDay()
		for i := 30; i < 60; i++ {
			d.Bars[i].Close = 1010000
			if direction == "short" {
				d.Bars[i].Close = 990000
			}
		}
		got, e := VWAPExit(context.Background(), d, direction)
		if e != nil || !got.At.Equal(marketdata.Clock(d.Date, 10, 0)) || got.Reason != "fixed_morning_time_exit" {
			t.Fatal(got, e)
		}
		d.Bars = d.Bars[4:]
		if _, e := VWAPExit(context.Background(), d, direction); e != nil {
			t.Fatal("valid delayed opening rejected", e)
		}
		d.Bars = d.Bars[1:]
		if _, e := VWAPExit(context.Background(), d, direction); e == nil {
			t.Fatal("late open admitted")
		}
	}
	d := exitDay()
	d.Bars[10].TurnoverJPY = "0"
	if _, e := VWAPExit(context.Background(), d, "long"); e == nil {
		t.Fatal("invalid turnover admitted")
	}
}
