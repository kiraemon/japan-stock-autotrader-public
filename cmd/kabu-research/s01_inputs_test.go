package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

func writeS01FixtureJSON(t *testing.T, path string, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil || os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("synthetic fixture write")
	}
	return raw
}

func s01Fixture(t *testing.T, plan []byte) s01InputManifest {
	t.Helper()
	universeFixture(t)
	if err := run(context.Background(), []string{"select-universe", "-input", "inputs/manifest.json"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob("data/research/universe-*.json")
	if len(paths) != 1 {
		t.Fatal("universe report missing")
	}
	uRaw, _ := os.ReadFile(paths[0])
	var u universeOutput
	if json.Unmarshal(uRaw, &u) != nil {
		t.Fatal("universe fixture parse")
	}
	dRaw, _ := os.ReadFile(u.DailyCatalog)
	var daily universeDailyCatalog
	if json.Unmarshal(dRaw, &daily) != nil {
		t.Fatal("daily fixture parse")
	}
	n := s01Normalization{Version: 1, Universe: paths[0], UniverseSHA: hash(uRaw), Complete: true}
	for _, code := range u.Universe.Union {
		symbol := code[:4] + "@1"
		d := marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: symbol, Environment: "production", Origin: "observed", Converter: "fabricated-test", Source: marketdata.Source{Format: "jquants-bulk-minute-v0.1", SHA256: strings.Repeat("a", 64), Label: "start", ReceivedAt: marketdata.Clock("2026-07-01", 16, 0)}}
		for _, row := range daily.Bars {
			if row.Code != code {
				continue
			}
			day := marketdata.Day{Date: row.Date, Issues: []string{"source_coverage_unverified"}}
			for minute := 0; minute < 5; minute++ {
				v := int64(100)
				day.Bars = append(day.Bars, marketdata.Bar{Start: marketdata.Clock(row.Date, 9, minute), Open: 1000000, High: 1020000, Low: 990000, Close: 1010000, Volume: &v})
			}
			d.Days = append(d.Days, day)
		}
		path := filepath.Join("inputs", code+".json")
		raw := writeS01FixtureJSON(t, path, d)
		n.Records = append(n.Records, s01MinuteFile{Month: "2026-06", Symbol: symbol, Path: path, SHA: hash(raw), SourceSHA: d.Source.SHA256, Days: len(d.Days), Bars: len(d.Days) * 5})
	}
	nRaw := writeS01FixtureJSON(t, "inputs/normalized.json", n)
	// This reconciliation is fabricated test evidence, never an observed claim.
	cRaw := writeS01FixtureJSON(t, "inputs/reconciliation.json", map[string]any{"version": "daily-minute-reconciliation-v0.2", "normalization_index_sha256": hash(nRaw), "daily_catalog_sha256": u.DailySHA, "files": 30, "matched_stock_days": 660, "bars": 3300, "boundary_events": 0, "boundary_only_stock_days": 0, "issues": []string{}, "quality_promoted": false, "new_pnl_evaluation": false})
	if os.WriteFile("inputs/plan.json", plan, 0600) != nil {
		t.Fatal("plan fixture write")
	}
	m := s01InputManifest{Version: 1, Universe: filepath.Join("..", paths[0]), UniverseSHA: hash(uRaw), Normalization: "normalized.json", NormalizationSHA: hash(nRaw), Reconciliation: "reconciliation.json", ReconciliationSHA: hash(cRaw), Plan: "plan.json", PlanSHA: hash(plan)}
	writeS01FixtureJSON(t, "inputs/s01.json", m)
	return m
}

func TestS01InputsStableCausalIndicatorsAndNoPromotion(t *testing.T) {
	plan, err := os.ReadFile("../../examples/research/s01-jp20.plan.json")
	if err != nil || hash(plan) != s01.PlanSHA256 {
		t.Fatal("fixed plan changed without a strategy version")
	}
	t.Chdir(t.TempDir())
	s01Fixture(t, plan)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"prepare-s01", "-input", "inputs/s01.json"}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "symbols=30 / cases=30 / indicators_available=30 / rvol_passed_with_available_inputs=30") || strings.Contains(out.String(), "1000000") {
			t.Fatal("summary or counts changed")
		}
	}
	paths, _ := filepath.Glob("data/research/s01-input-index-*.json")
	a, _ := os.ReadFile(paths[0])
	b, _ := os.ReadFile(paths[1])
	if !bytes.Equal(a, b) {
		t.Fatal("repeat changed input hashes")
	}
	var index s01InputIndex
	if json.Unmarshal(a, &index) != nil || index.Quality || index.NewPNL || index.TradingEligible || len(index.Files) != 30 {
		t.Fatal("quality or provenance lost")
	}
	for _, file := range index.Files {
		raw, _ := os.ReadFile(file.Path)
		var stock s01StockInputs
		if hash(raw) != file.SHA || json.Unmarshal(raw, &stock) != nil || stock.Quality || stock.TradingEligible || stock.NewPNL || len(stock.Cases) != 1 {
			t.Fatal("artifact contract changed")
		}
		row := stock.Cases[0]
		if *row.ATR.Value != "30000" || *row.ATR.StopDistance != 3000 || *row.Opening.RelativeVolume != "1" || len(row.ATR.Previous) != 15 || row.ATR.BasisDate != "2026-06-29" || len(row.Opening.Previous) != 14 || row.Opening.Target.SourceIssues[0] != "source_coverage_unverified" {
			t.Fatal("indicator, reference or quality changed")
		}
	}
}

func TestS01InputsRejectTamperAndUnsafeEvidence(t *testing.T) {
	plan, err := os.ReadFile("../../examples/research/s01-jp20.plan.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"plan", "minute", "count", "reconciliation", "quality", "missing_calendar"} {
		t.Run(fault, func(t *testing.T) {
			t.Chdir(t.TempDir())
			m := s01Fixture(t, plan)
			switch fault {
			case "plan":
				_ = os.WriteFile("inputs/plan.json", append(plan, ' '), 0600)
			case "minute":
				_ = os.WriteFile("inputs/00000.json", []byte("private-marker"), 0600)
			case "reconciliation":
				m.ReconciliationSHA = strings.Repeat("0", 64)
			case "count", "quality":
				raw, _ := os.ReadFile("inputs/normalized.json")
				var n s01Normalization
				_ = json.Unmarshal(raw, &n)
				if fault == "quality" {
					n.Quality = true
				} else {
					n.Records[0].Bars++
				}
				m.NormalizationSHA = hash(writeS01FixtureJSON(t, "inputs/normalized.json", n))
				c := map[string]any{}
				raw, _ = os.ReadFile("inputs/reconciliation.json")
				_ = json.Unmarshal(raw, &c)
				c["normalization_index_sha256"] = m.NormalizationSHA
				m.ReconciliationSHA = hash(writeS01FixtureJSON(t, "inputs/reconciliation.json", c))
			case "missing_calendar":
				_ = os.Remove("inputs/calendar.json")
			}
			writeS01FixtureJSON(t, "inputs/s01.json", m)
			if err := run(context.Background(), []string{"prepare-s01", "-input", "inputs/s01.json"}, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatal("tamper admitted or private input leaked")
			}
		})
	}
	if err := run(context.Background(), []string{"prepare-s01", "-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
