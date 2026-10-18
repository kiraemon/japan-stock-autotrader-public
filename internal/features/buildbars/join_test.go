package buildbars

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

func joinFixture(date, source string) marketdata.Dataset {
	v := int64(100)
	return marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: "0000@1", Origin: "observed", Environment: "production", Converter: "fixture", Source: marketdata.Source{Format: "jquants-bulk-minute-v0.1", Label: "start", SHA256: strings.Repeat(source, 64), ReceivedAt: marketdata.Clock("2026-07-01", 16, 0)}, Days: []marketdata.Day{{Date: date, Issues: []string{"coverage_unverified"}, Bars: []marketdata.Bar{{Start: marketdata.Clock(date, 9, 0), Open: 100, High: 102, Low: 99, Close: 101, Volume: &v}}, Events: []marketdata.Event{{Time: marketdata.Clock(date, 15, 30), Price: 101, Volume: &v}}}}}
}
func TestJoinPreservesMissingDatesProvenanceOrderAndInputOwnership(t *testing.T) {
	a, b := joinFixture("2026-05-29", "a"), joinFixture("2026-06-02", "b")
	before, _ := json.Marshal([]marketdata.Dataset{a, b})
	r, err := JoinJQuants(context.Background(), []marketdata.Dataset{b, a})
	if err != nil || len(r.Days) != 2 || r.Days[0].Date != "2026-05-29" || r.Days[1].Date != "2026-06-02" || r.Source.Format != marketdata.JQuantsJoinedFormat || r.Source.SHA256 == a.Source.SHA256 || r.Days[0].Issues[0] != "coverage_unverified" {
		t.Fatal("join changed contract", err)
	}
	again, err := JoinJQuants(context.Background(), []marketdata.Dataset{a, b})
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("join order changed output", err)
	}
	r.Days[0].Issues[0] = "mutated"
	*r.Days[0].Bars[0].Volume = 0
	*r.Days[0].Events[0].Volume = 0
	after, _ := json.Marshal([]marketdata.Dataset{a, b})
	if string(before) != string(after) {
		t.Fatal("join shares mutable input state")
	}
}
func TestJoinRejectsDuplicateDatesSourcesAndOtherProviders(t *testing.T) {
	a, b := joinFixture("2026-05-29", "a"), joinFixture("2026-05-29", "b")
	for _, inputs := range [][]marketdata.Dataset{nil, {a, a}, {a, b}} {
		if _, err := JoinJQuants(context.Background(), inputs); err == nil {
			t.Fatal("invalid join accepted")
		}
	}
	b.Days[0].Date = "2026-06-02"
	b.Days[0].Bars[0].Start = marketdata.Clock("2026-06-02", 9, 0)
	b.Source.Format = "chart-csv"
	if _, err := JoinJQuants(context.Background(), []marketdata.Dataset{a, b}); err == nil {
		t.Fatal("different source accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := JoinJQuants(ctx, []marketdata.Dataset{a}); err == nil {
		t.Fatal("cancel ignored")
	}
}
