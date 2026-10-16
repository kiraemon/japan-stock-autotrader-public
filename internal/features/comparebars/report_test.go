package comparebars

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

func fixture() marketdata.Dataset {
	v := int64(100)
	d := marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: "7203@1", Environment: "production", Origin: "observed", Converter: "test", Source: marketdata.Source{Format: "test", SHA256: strings.Repeat("a", 64), Label: "start", ReceivedAt: marketdata.Clock("2026-10-16", 9, 3).Add(30 * time.Second)}}
	day := marketdata.Day{Date: "2026-10-16", Issues: []string{"coverage_unverified"}}
	for m := 0; m < 5; m++ {
		day.Bars = append(day.Bars, marketdata.Bar{Start: marketdata.Clock(day.Date, 9, m), Open: 100, High: 102, Low: 99, Close: 101, Volume: &v})
	}
	d.Days = []marketdata.Day{day}
	return d
}

func TestComparisonSeparatesUnknownUnfinishedMismatchAndNonOverlap(t *testing.T) {
	a, b := fixture(), fixture()
	a.Days[0].Bars = a.Days[0].Bars[:4]
	b.Days[0].Bars = b.Days[0].Bars[1:]
	b.Days[0].Bars[0].Start = b.Days[0].Bars[0].Start.UTC()
	b.Days[0].Bars[1].Close = 100
	b.Days[0].Bars[1].Volume = nil
	before := fixture()
	r, err := Compare(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	d := r.Days[0]
	if d.Compared != 2 || d.AllMatches != 1 || d.Unfinished != 1 || d.OnlyInput != 1 || d.OnlyReference != 1 || d.OpenMatches != 2 || d.CloseMatches != 1 || d.VolumeCompared != 1 || d.VolumeMatches != 1 || len(d.MismatchMinutes) != 1 || r.QualityPromoted {
		t.Fatalf("unexpected counts: %+v", d)
	}
	if !reflect.DeepEqual(a.Days[0].Issues, before.Days[0].Issues) {
		t.Fatal("comparison promoted input quality")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compare(ctx, a, b); err == nil {
		t.Fatal("cancel ignored")
	}
	b.Symbol = "8306@1"
	if _, err := Compare(context.Background(), a, b); err == nil {
		t.Fatal("different symbol compared")
	}
}

func TestComparisonIncludesReferenceOnlyDaysAndExplicitUnfinished(t *testing.T) {
	a, b := fixture(), fixture()
	a.Source.ReceivedAt = marketdata.Clock("2026-10-16", 16, 0)
	b.Source.ReceivedAt = a.Source.ReceivedAt
	b.Days[0].Bars[0].Issues = []string{"unfinished_minute"}
	b.Days = append([]marketdata.Day{{Date: "2026-10-15"}}, b.Days...)
	r, err := Compare(context.Background(), a, b)
	if err != nil || len(r.Days) != 2 || r.Days[0].Date != "2026-10-15" || r.Days[1].Unfinished != 1 || r.Days[1].AllMatches != 4 {
		t.Fatalf("report=%+v err=%v", r, err)
	}
	a.Source.SHA256 = "invalid"
	if _, err := Compare(context.Background(), a, b); err == nil {
		t.Fatal("invalid source accepted")
	}
}
