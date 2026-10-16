package backtest

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func diagnosticFixture(side r001.Direction) (marketdata.Dataset, marketdata.Dataset) {
	csv := Synthetic(side)
	csv.Origin = "observed" // Generated fixture, not actual market data.
	csv.Source.Format = "kabu-chart-csv-utf8"
	csv.Days[0].Issues = []string{"coverage_unverified", "csv_semantics_unverified", "trading_status_unverified"}
	raw, _ := json.Marshal(csv)
	var history marketdata.Dataset
	_ = json.Unmarshal(raw, &history)
	history.Source.Format = "kabu-timeandsales"
	history.Source.SHA256 = strings.Repeat("b", 64)
	history.Days[0].Issues = []string{"quantity_semantics_unverified", "source_crosscheck_unverified"}
	history.Days[0].Bars[0].Issues = []string{"open_order_unverified", "close_order_unverified"}
	return csv, history
}

func TestDiagnosticFullWindowMatchesWithoutPromotingOriginalData(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		csv, history := diagnosticFixture(side)
		beforeCSV, _ := json.Marshal(csv)
		beforeHistory, _ := json.Marshal(history)
		r, err := Diagnose(context.Background(), csv, history, side)
		if err != nil {
			t.Fatal(err)
		}
		check := r.Diagnostic.Days[0]
		if !check.Eligible || check.Matched != 111 || check.Required != 111 || r.Days[0].Trade == nil || r.Scope != "crosschecked_price_pattern_diagnostic" || r.StrategyAdopted || r.PortfolioMode || r.ShortAvailabilityVerified || len(r.Diagnostic.Limitations) == 0 {
			t.Fatalf("scope or eligibility failed: %+v", r)
		}
		if len(check.CSVDayIssues) != 3 || len(check.HistoryDayIssues) != 2 || r.Diagnostic.CSVNormalizedSHA256 == "" || r.Diagnostic.HistoryNormalizedSHA256 == "" {
			t.Fatal("quality/provenance lost")
		}
		again, err := Diagnose(context.Background(), csv, history, side)
		if err != nil || !reflect.DeepEqual(r, again) {
			t.Fatal("diagnostic replay changed")
		}
		afterCSV, _ := json.Marshal(csv)
		afterHistory, _ := json.Marshal(history)
		if string(beforeCSV) != string(afterCSV) || string(beforeHistory) != string(afterHistory) {
			t.Fatal("source quality/data mutated")
		}
		normal := runFixture(t, csv, side)
		if normal.Days[0].Status != "unevaluable" || normal.Days[0].Trade != nil {
			t.Fatal("normal gate bypassed")
		}
	}
}

func TestDiagnosticRejectsIncompleteWindowEvenAfterEarlierWinningTrade(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*marketdata.Dataset, *marketdata.Dataset)
	}{
		{"late_missing", "required_crosscheck_minute_missing", func(a, b *marketdata.Dataset) { b.Days[0].Bars = b.Days[0].Bars[:110] }},
		{"late_mismatch", "required_crosscheck_ohlcv_mismatch", func(a, b *marketdata.Dataset) { b.Days[0].Bars[110].High++ }},
		{"unfinished", "required_crosscheck_minute_unfinished", func(a, b *marketdata.Dataset) {
			b.Source.ReceivedAt = marketdata.Clock("2026-10-14", 10, 50).Add(30 * time.Second)
		}},
		{"unknown_volume", "required_crosscheck_volume_unknown_or_zero", func(a, b *marketdata.Dataset) { b.Days[0].Bars[110].Volume = nil }},
		{"zero_volume", "required_crosscheck_volume_unknown_or_zero", func(a, b *marketdata.Dataset) {
			v := int64(0)
			b.Days[0].Bars[110].Volume = &v
			a.Days[0].Bars[110].Volume = &v
		}},
		{"unknown_quality", "unsupported_day_quality_issue", func(a, b *marketdata.Dataset) {
			a.Days[0].Issues = append(a.Days[0].Issues, "synthetic_unknown_quality")
		}},
		{"unknown_minute", "unsupported_minute_quality_issue", func(a, b *marketdata.Dataset) { b.Days[0].Bars[110].Issues = []string{"synthetic_unknown_quality"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := diagnosticFixture(r001.Long)
			tc.change(&a, &b)
			r, err := Diagnose(context.Background(), a, b, r001.Long)
			if err != nil {
				t.Fatal(err)
			}
			if r.Diagnostic.Days[0].Eligible || r.Days[0].Status != "unevaluable" || r.Days[0].Trade != nil || r.Costs[0].Trades != 0 || !contains(r.Days[0].Reasons, tc.reason) {
				t.Fatalf("incomplete day silently admitted: %+v", r)
			}
		})
	}
}

func TestDiagnosticSourceConstraintsAndCancellation(t *testing.T) {
	for _, change := range []func(*marketdata.Dataset, *marketdata.Dataset){
		func(a, b *marketdata.Dataset) { a.Origin = "synthetic"; a.Source.Format = "synthetic" },
		func(a, b *marketdata.Dataset) { b.Symbol = "8306@1" },
		func(a, b *marketdata.Dataset) { b.Environment = "production" },
		func(a, b *marketdata.Dataset) { a.Source.Format = "chart-csv" },
		func(a, b *marketdata.Dataset) { a.Source.Label = "end" },
		func(a, b *marketdata.Dataset) { b.Source.SHA256 = a.Source.SHA256 },
	} {
		a, b := diagnosticFixture(r001.Long)
		change(&a, &b)
		if _, err := Diagnose(context.Background(), a, b, r001.Long); err == nil {
			t.Fatal("unsupported sources accepted")
		}
	}
	a, b := diagnosticFixture(r001.Long)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Diagnose(ctx, a, b, r001.Long); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := Diagnose(context.Background(), a, b, "bad"); err == nil {
		t.Fatal("invalid direction accepted")
	}
	b.Days[0].Date = "2026-10-15"
	b.Days[0].Bars = nil
	r, err := Diagnose(context.Background(), a, b, r001.Long)
	if err != nil || !contains(r.Days[0].Reasons, "reference_day_missing") {
		t.Fatal("missing reference day lost")
	}
}
