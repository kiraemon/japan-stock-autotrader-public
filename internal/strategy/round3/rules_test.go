package round3

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func day(date string, open, close int64) marketdata.Day {
	d := marketdata.Day{Date: date}
	for minute := 0; minute < 30; minute++ {
		v := int64(100)
		d.Bars = append(d.Bars, marketdata.Bar{Start: marketdata.Clock(date, 9, minute), Open: open * 10000, High: 104 * 10000, Low: 96 * 10000, Close: close * 10000, Volume: &v, TurnoverJPY: "10200"})
	}
	return d
}
func noiseFixture() (string, []string, []marketdata.Day, marketdata.Day) {
	date := "2026-06-30"
	dates := []string{}
	for at := marketdata.Clock("2026-06-10", 0, 0); marketdata.Date(at) < date; at = at.AddDate(0, 0, 1) {
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			dates = append(dates, marketdata.Date(at))
		}
	}
	if len(dates) != 14 {
		panic("fabricated fixture needs fourteen dates")
	}
	past := []marketdata.Day{}
	for _, d := range dates {
		past = append(past, day(d, 100, 100))
	}
	return date, dates, past, day(date, 100, 103)
}
func TestNoiseExactAmountStrictBandsAndCausalClock(t *testing.T) {
	date, dates, past, current := noiseFixture()
	before, _ := json.Marshal([]any{dates, past, current})
	r, err := NoiseArea(context.Background(), date, dates, past, current, 1010000)
	if err != nil || r.Decision.Status != "candidate" || r.Decision.Direction != "long" || r.Sigma != "0" || r.VWAP != "102" || r.Upper != "101" || r.Lower != "100" || !r.Decision.SignalAt.Equal(marketdata.Clock(date, 9, 30)) || !r.Decision.EntryAt.Equal(r.Decision.SignalAt) || !r.Decision.ExitAt.Equal(marketdata.Clock(date, 10, 0)) {
		t.Fatal("fixed noise rule", err)
	}
	after, _ := json.Marshal([]any{dates, past, current})
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	// Arbitrary later prices and an unrelated future date cannot alter the signal.
	current.Bars = append(current.Bars, marketdata.Bar{Start: marketdata.Clock(date, 9, 30), Open: 1, Close: 999999999})
	past = append(past, day("2026-07-01", 100, 103))
	later, err := NoiseArea(context.Background(), date, dates, past, current, 1010000)
	if err != nil || !reflect.DeepEqual(r, later) {
		t.Fatal("future prices leaked into signal", err)
	}
	current = day(date, 100, 102)
	equal, err := NoiseArea(context.Background(), date, dates, past, current, 1000000)
	if err != nil || equal.Decision.Status != "no_trade" {
		t.Fatal("VWAP equality became breakout")
	}
	for i := range current.Bars {
		current.Bars[i].TurnoverJPY = "9700"
		current.Bars[i].Close = 96 * 10000
	}
	short, err := NoiseArea(context.Background(), date, dates, past, current, 1000000)
	if err != nil || short.Decision.Status != "candidate" || short.Decision.Direction != "short" || short.VWAP != "97" {
		t.Fatal("short rule", err)
	}
}
func TestNoiseFourteenSameClockRatiosNotDailyReturnsOrCurrentDay(t *testing.T) {
	date, dates, past, current := noiseFixture()
	for i := range past {
		past[i].Bars[29].Close = 102 * 10000
	}
	r, err := NoiseArea(context.Background(), date, dates, past, current, 1010000)
	if err != nil || r.Sigma != "1/50" || r.Upper != "5151/50" || r.Lower != "98" || r.Decision.Status != "no_trade" {
		t.Fatal("exact historical same-clock bands", err)
	}
	current.Bars[29].Close = 104 * 10000
	r, err = NoiseArea(context.Background(), date, dates, past, current, 1010000)
	if err != nil || r.Decision.Status != "candidate" || r.Sigma != "1/50" {
		t.Fatal("current return contaminated history", err)
	}
}
func TestNoiseMissingReferencesAndAmountsStayUnavailable(t *testing.T) {
	for _, fault := range []string{"history_day", "history_0929", "current_0929", "late_open", "turnover", "unfinished", "unknown_day"} {
		date, dates, past, current := noiseFixture()
		switch fault {
		case "history_day":
			past = past[1:]
		case "history_0929":
			past[0].Bars = past[0].Bars[:29]
		case "current_0929":
			current.Bars = current.Bars[:29]
		case "late_open":
			current.Bars = current.Bars[5:]
		case "turnover":
			current.Bars[1].TurnoverJPY = ""
		case "unfinished":
			current.Bars[29].Issues = []string{"unfinished_minute"}
		case "unknown_day":
			current.Issues = []string{"unknown"}
		}
		r, err := NoiseArea(context.Background(), date, dates, past, current, 1010000)
		if err != nil || r.Decision.Status != "unevaluable" || r.Decision.Reason == "" {
			t.Fatal("missing information converted to price/signal", fault, err)
		}
	}
	date, dates, past, current := noiseFixture()
	dates[13] = date
	if _, err := NoiseArea(context.Background(), date, dates, past, current, 1000000); err == nil {
		t.Fatal("current day used as prior reference")
	}
	date, dates, past, current = noiseFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NoiseArea(ctx, date, dates, past, current, 1000000); err == nil {
		t.Fatal("cancel ignored")
	}
}
func TestGapTwoPercentStrictThresholdAndNextMinuteTiming(t *testing.T) {
	for _, tt := range []struct {
		open                   int64
		status, direction, gap string
	}{{102, "no_trade", "", "1/50"}, {98, "no_trade", "", "-1/50"}, {103, "candidate", "short", "3/100"}, {97, "candidate", "long", "-3/100"}} {
		date := "2026-06-30"
		current := day(date, tt.open, 100)
		// Later bars do not supply another opening or execution price.
		for i := 1; i < len(current.Bars); i++ {
			current.Bars[i].Open = 1
		}
		r, err := GapReversal(context.Background(), date, current, 1000000)
		if err != nil || r.Decision.Status != tt.status || r.Decision.Direction != tt.direction || r.Gap != tt.gap || !r.Decision.SignalAt.Equal(marketdata.Clock(date, 9, 1)) || !r.Decision.EntryAt.Equal(r.Decision.SignalAt) || !r.Decision.ExitAt.Equal(marketdata.Clock(date, 9, 30)) {
			t.Fatal("gap rule or timing", err)
		}
	}
	current := day("2026-06-30", 103, 100)
	current.Bars = current.Bars[1:]
	r, err := GapReversal(context.Background(), current.Date, current, 1000000)
	if err != nil || r.Decision.Status != "unevaluable" || r.Decision.Reason != "actual_0900_minute_missing" {
		t.Fatal("late actual opening substituted")
	}
}
