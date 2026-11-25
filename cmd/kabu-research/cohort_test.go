package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

func writeCohortFixture(t *testing.T) cohortManifest {
	t.Helper()
	if err := os.MkdirAll("inputs", 0700); err != nil {
		t.Fatal(err)
	}
	d := backtest.Synthetic(r001.Long)
	var csv strings.Builder
	csv.WriteString("日付,始値,高値,安値,終値,売買高\n")
	rows := []map[string]any{}
	for _, b := range d.Days[0].Bars {
		fmt.Fprintf(&csv, "%s,%d,%d,%d,%d,400\n", b.Start.Format("2006/01/02 15:04:05"), b.Open/10000, b.High/10000, b.Low/10000, b.Close/10000)
		for i, p := range []int64{b.Open, b.High, b.Low, b.Close} {
			rows = append(rows, map[string]any{"Time": b.Start.Add(time.Duration(i*15) * time.Second), "Price": p / 10000, "Volume": 100})
		}
	}
	at := d.Source.ReceivedAt
	history, _ := json.Marshal(map[string]any{"schema_version": 1, "environment": "test", "symbol": "0000@1", "complete": true, "started_at": at, "history_received_at": at, "finished_at": at, "time_and_sales": map[string]any{"Symbol": "0000", "Exchange": 1, "TradingPriceCount": len(rows), "TradingPrice": rows}})
	if os.WriteFile("inputs/chart.csv", []byte(csv.String()), 0600) != nil || os.WriteFile("inputs/history.json", history, 0600) != nil {
		t.Fatal("fixture write")
	}
	return cohortManifest{Version: 1, Environment: "test", Dates: []string{d.Days[0].Date}, Sources: []cohortSource{{Symbol: "0000@1", CSV: "chart.csv", History: "history.json", CapturedAt: at.Format(time.RFC3339)}}}
}

func TestCohortRawSourceComparisonMissingDatesProvenanceAndReplay(t *testing.T) {
	t.Chdir(t.TempDir())
	m := writeCohortFixture(t)
	missing := "2026-10-13"
	if m.Dates[0] == missing {
		missing = "2026-10-12"
	}
	m.Dates = append(m.Dates, missing)
	raw, _ := json.Marshal(m)
	if os.WriteFile("inputs/cohort.json", raw, 0600) != nil {
		t.Fatal("manifest write")
	}
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"cohort-check", "-input", "inputs/cohort.json"}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "diagnostic_cases=8") || !strings.Contains(out.String(), "unevaluable=1") || strings.Contains(out.String(), "entry_price") {
			t.Fatal("missing day or price disclosure")
		}
	}
	paths, _ := filepath.Glob("data/research/cohort-review-*.json")
	if len(paths) != 2 {
		t.Fatal("reports missing or overwritten")
	}
	a, _ := os.ReadFile(paths[0])
	b, _ := os.ReadFile(paths[1])
	if !bytes.Equal(a, b) {
		t.Fatal("report not reproducible")
	}
	var report cohortReport
	if json.Unmarshal(a, &report) != nil {
		t.Fatal("report decode")
	}
	if len(report.Diagnostics) != 4 || len(report.Sources) != 1 || len(report.Sources[0].CSVCoverage) != 1 || len(report.Summary.Dates) != 2 || report.Summary.UnseenHoldout {
		t.Fatal("scope or original coverage changed")
	}
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.StrategyAdopted || diagnostic.Scope != "crosschecked_price_pattern_diagnostic" {
			t.Fatal("quality promoted")
		}
		for _, day := range diagnostic.Days {
			if day.Date == missing && (day.Status != "unevaluable" || len(day.Reasons) == 0) {
				t.Fatal("absent date admitted")
			}
		}
	}
	for _, g := range report.Summary.Groups {
		if g.Cases != 2 || g.Eligible != 1 || g.Unevaluable != 1 {
			t.Fatal("grid coverage changed")
		}
	}
	csvRaw, _ := os.ReadFile("inputs/chart.csv")
	historyRaw, _ := os.ReadFile("inputs/history.json")
	if report.ManifestSHA256 != hash(raw) || report.Sources[0].CSVSHA256 != hash(csvRaw) || report.Sources[0].HistorySHA256 != hash(historyRaw) {
		t.Fatal("raw source hashes lost")
	}
	runs, _ := filepath.Glob("data/research/research-run-*.json")
	for _, p := range runs {
		var meta struct {
			InputSHA256  string `json:"input_sha256"`
			ReportSHA256 string `json:"report_sha256"`
		}
		value, _ := os.ReadFile(p)
		if json.Unmarshal(value, &meta) != nil || meta.InputSHA256 != hash(raw) || meta.ReportSHA256 != hash(a) {
			t.Fatal("run hashes lost")
		}
	}
}

func TestCohortManifestErrorsAndNoRawErrorDisclosure(t *testing.T) {
	t.Chdir(t.TempDir())
	m := writeCohortFixture(t)
	raw, _ := json.Marshal(m)
	invalid := [][]byte{append(raw, []byte(" {}")...), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1), bytes.Replace(raw, []byte(`"environment":"test"`), []byte(`"environment":"secret-token-value"`), 1), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"quality_override":true`), 1)}
	for _, value := range invalid {
		if _, err := decodeCohort(value); err == nil || strings.Contains(err.Error(), "secret-token-value") {
			t.Fatal("invalid manifest or source disclosure")
		}
	}
	m.Dates = append(m.Dates, m.Dates[0])
	value, _ := json.Marshal(m)
	if _, err := decodeCohort(value); err == nil {
		t.Fatal("duplicate dates admitted")
	}
	m.Dates = m.Dates[:1]
	m.Sources = append(m.Sources, m.Sources[0])
	value, _ = json.Marshal(m)
	if _, err := decodeCohort(value); err == nil {
		t.Fatal("duplicate symbol admitted")
	}
	if err := run(context.Background(), []string{"cohort-check", "-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"cohort-check"}, {"cohort-check", "-input", "inputs/cohort.json", "-direction", "long"}, {"cohort-check", "-input", "missing.json"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid args admitted")
		}
	}
}
