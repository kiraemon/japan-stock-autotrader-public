package prepareorb

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

func fixture() (marketdata.Dataset, string, []string) {
	date := "2026-06-22"
	previous := []string{}
	for at := marketdata.Clock(date, 0, 0).AddDate(0, 0, -1); len(previous) < Lookback; at = at.AddDate(0, 0, -1) {
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			previous = append([]string{marketdata.Date(at)}, previous...)
		}
	}
	d := marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: "0000@1", Environment: "test", Origin: "synthetic", Converter: "fixture", Source: marketdata.Source{Format: "synthetic", SHA256: strings.Repeat("a", 64), Label: "start", ReceivedAt: marketdata.Clock(date, 16, 0)}}
	for _, day := range append(append([]string{}, previous...), date) {
		row := marketdata.Day{Date: day, Bars: []marketdata.Bar{}}
		for i := 0; i < 5; i++ {
			volume := int64(10)
			if day == date {
				volume = 20
			}
			row.Bars = append(row.Bars, marketdata.Bar{Start: marketdata.Clock(day, 9, i), Open: 100000, High: int64(110000 + i), Low: int64(90000 - i), Close: int64(100000 + i), Volume: &volume})
		}
		d.Days = append(d.Days, row)
	}
	return d, date, previous
}

func TestExactVolumeRangeNoFutureLeakageOrMutation(t *testing.T) {
	d, date, sessions := fixture()
	before, _ := json.Marshal(d)
	r, err := Check(context.Background(), d, date, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if !r.CalculationAvailable || *r.RelativeVolume != "2" || *r.PreviousMean != "50" || !*r.AtLeastOne || *r.Target.Volume != "100" || r.CompletePrevious != 14 || r.Target.Range.High != 110004 || r.Target.Range.Low != 89996 || r.Target.Range.Close != 100004 {
		t.Fatalf("wrong opening metrics: %+v", r)
	}
	if r.TradingEligible || r.StrategyAdopted || r.QualityPromoted {
		t.Fatal("input diagnostic became trading permission")
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	// A huge move/volume after 09:05 must not affect the opening calculation.
	v := int64(math.MaxInt64)
	d.Days[14].Bars = append(d.Days[14].Bars, marketdata.Bar{Start: marketdata.Clock(date, 10, 0), Open: 100, High: marketdata.MaxPrice, Low: 1, Close: 100, Volume: &v})
	again, err := Check(context.Background(), d, date, sessions)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("post-opening data changed diagnostic", err)
	}
}

func TestMissingOpeningNeverShortensLookback(t *testing.T) {
	for _, c := range []struct {
		name   string
		index  int
		change func(*marketdata.Day)
	}{
		{"missing_day", 2, func(day *marketdata.Day) { day.Bars = nil }},
		{"missing_minute", 2, func(day *marketdata.Day) { day.Bars = day.Bars[1:] }},
		{"missing_volume", 2, func(day *marketdata.Day) { day.Bars[0].Volume = nil }},
		{"unfinished_reference", 2, func(day *marketdata.Day) { day.Bars[0].Issues = []string{"unfinished_minute"} }},
		{"target_missing", 14, func(day *marketdata.Day) { day.Bars = nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, date, sessions := fixture()
			c.change(&d.Days[c.index])
			r, err := Check(context.Background(), d, date, sessions)
			if err != nil || r.CalculationAvailable || r.RelativeVolume != nil || r.PreviousMean != nil || r.AtLeastOne != nil || len(r.Previous) != 14 {
				t.Fatal("missing data admitted", err)
			}
			if c.index < 14 && (r.CompletePrevious != 13 || len(r.MissingPrevious) != 1 || r.MissingPrevious[0] != sessions[c.index]) {
				t.Fatal("reference date disappeared")
			}
		})
	}
	d, date, sessions := fixture()
	d.Source.ReceivedAt = marketdata.Clock(date, 9, 4).Add(30 * time.Second)
	r, err := Check(context.Background(), d, date, sessions)
	if err != nil || r.CalculationAvailable || r.Target.Volume != nil {
		t.Fatal("unclosed opening admitted", err)
	}
	d, date, sessions = fixture()
	d.Days = d.Days[:14]
	r, err = Check(context.Background(), d, date, sessions)
	if err != nil || r.Target.Volume != nil || len(r.Target.MissingMinutes) != 5 {
		t.Fatal("absent target lost", err)
	}
}

func TestZeroFractionBoundaryAndLargeVolumes(t *testing.T) {
	d, date, sessions := fixture()
	for i := range d.Days {
		for j := range d.Days[i].Bars {
			v := int64(0)
			d.Days[i].Bars[j].Volume = &v
		}
	}
	r, err := Check(context.Background(), d, date, sessions)
	if err != nil || r.CalculationAvailable || len(r.Problems) != 1 || r.Problems[0] != "zero_reference_volume" {
		t.Fatal("zero denominator admitted", err)
	}
	for i := range d.Days {
		for j := range d.Days[i].Bars {
			v := int64(math.MaxInt64)
			d.Days[i].Bars[j].Volume = &v
		}
	}
	r, err = Check(context.Background(), d, date, sessions)
	if err != nil || !r.CalculationAvailable || *r.RelativeVolume != "1" || !*r.AtLeastOne || *r.Target.Volume != "46116860184273879035" {
		t.Fatal("large volume overflow or boundary", err)
	}
	for i := range d.Days {
		for j := range d.Days[i].Bars {
			v := int64(0)
			d.Days[i].Bars[j].Volume = &v
		}
	}
	one := int64(1)
	d.Days[0].Bars[0].Volume = &one
	r, err = Check(context.Background(), d, date, sessions)
	if err != nil || *r.PreviousMean != "1/14" || *r.RelativeVolume != "0" || *r.AtLeastOne {
		t.Fatal("fractional reference or zero target", err)
	}
}

func TestCalendarValidationCancellationAndObservedQuality(t *testing.T) {
	d, date, sessions := fixture()
	for _, change := range []func([]string) []string{
		func(s []string) []string { return s[:13] },
		func(s []string) []string { s[1] = s[0]; return s },
		func(s []string) []string { s[0], s[1] = s[1], s[0]; return s },
		func(s []string) []string { s[13] = date; return s },
		func(s []string) []string { s[13] = "2026-06-20"; return s },
		func(s []string) []string { s[13] = "2026-06-23"; return s },
	} {
		if _, err := Check(context.Background(), d, date, change(append([]string{}, sessions...))); err == nil {
			t.Fatal("bad calendar admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Check(ctx, d, date, sessions); err == nil {
		t.Fatal("cancellation ignored")
	}
	d.Origin, d.Source.Format = "observed", "chart-csv"
	d.Days[14].Issues = []string{"trading_status_unverified"}
	r, err := Check(context.Background(), d, date, sessions)
	if err != nil || !r.CalculationAvailable || r.QualityPromoted || r.TradingEligible || r.Target.SourceIssues[0] != "trading_status_unverified" {
		t.Fatal("quality state lost", err)
	}
	d.Symbol = "0000@3"
	if _, err := Check(context.Background(), d, date, sessions); err == nil {
		t.Fatal("non-TSE admitted")
	}
}
