package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/jquantsbulk"
)

func universeFixture(t *testing.T) universeManifest {
	t.Helper()
	if os.MkdirAll("inputs", 0700) != nil {
		t.Fatal("mkdir")
	}
	writeJSON := func(path string, v any) []byte {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil || os.WriteFile(path, raw, 0600) != nil {
			t.Fatal("fixture JSON write")
		}
		return raw
	}
	rule := map[string]any{"version": 1, "rule_id": "s01-universe-v1", "evaluation_from": "2026-06-30", "evaluation_to": "2026-06-30", "product_category": "011", "markets": []string{"0111", "0112", "0113"}, "liquidity_lookback_sessions": 20, "universe_size": 30, "liquidity_tie_break": "Code ascending", "real_orders_enabled": false, "strategy_adopted": false}
	ruleRaw := writeJSON("inputs/rule.json", rule)
	rows := []map[string]string{}
	dates := []string{}
	for day := 1; day <= 30; day++ {
		at := time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)
		holiday := "0"
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			holiday = "1"
			dates = append(dates, at.Format("2006-01-02"))
		}
		rows = append(rows, map[string]string{"Date": at.Format("2006-01-02"), "HolDiv": holiday})
	}
	cal, _ := json.Marshal(map[string]any{"data": rows})
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	base := jquantsbulk.Report{Version: jquantsbulk.Version, Month: "2026-06", Complete: true, Started: at, Captured: at.Add(time.Second), Requests: 2, LastStatus: 200}
	calendar := base
	calendar.CalendarOnly = true
	calendar.Calendar = cal
	writeJSON("inputs/calendar.json", calendar)
	var daily, master strings.Builder
	daily.WriteString("Date,Code,O,H,L,C,UL,LL,Vo,Va,AdjFactor,MktCap,ExRT\n")
	master.WriteString("Date,Code,CoName,CoNameEn,S17,S17Nm,S33,S33Nm,ScaleCat,Mkt,MktNm,Mrgn,MrgnNm,ProdCat\n")
	for _, date := range dates {
		for i := 0; i < 32; i++ {
			fmt.Fprintf(&daily, "%s,%04d0,100,102,99,101,0,0,100,10000,1,,\n", date, i)
			fmt.Fprintf(&master, "%s,%04d0,synthetic,synthetic,1,a,2,b,none,0111,Prime,2,loan,011\n", date, i)
		}
	}
	for _, input := range []struct {
		kind jquantsbulk.Dataset
		body string
	}{{jquantsbulk.Daily, daily.String()}, {jquantsbulk.Master, master.String()}} {
		var buf bytes.Buffer
		z := gzip.NewWriter(&buf)
		_, _ = z.Write([]byte(input.body))
		_ = z.Close()
		filename := string(input.kind) + ".csv.gz"
		if os.WriteFile(filepath.Join("inputs", filename), buf.Bytes(), 0600) != nil {
			t.Fatal("fixture gzip")
		}
		capture := base
		capture.Dataset = input.kind
		prefix := "equities/master/"
		if input.kind == jquantsbulk.Daily {
			prefix = "equities/bars/daily/"
		}
		capture.Files = []jquantsbulk.File{{Key: prefix + "202606.csv.gz", Size: int64(buf.Len()), Local: filename, SHA256: hash(buf.Bytes())}}
		writeJSON("inputs/"+string(input.kind)+".json", capture)
	}
	m := universeManifest{Version: 1, RuleFile: "rule.json", RuleSHA: hash(ruleRaw), DailyFiles: []string{"daily.json"}, MasterFiles: []string{"master.json"}, CalendarFiles: []string{"calendar.json"}}
	writeJSON("inputs/manifest.json", m)
	return m
}

func TestUniverseCLIProvenanceStableCatalogAndSafeSummary(t *testing.T) {
	t.Chdir(t.TempDir())
	universeFixture(t)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"select-universe", "-input", "inputs/manifest.json"}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "days=1 / unique_codes=30") || strings.Contains(out.String(), "turnover_jpy_exact") || strings.Contains(out.String(), "1000000") {
			t.Fatal("unsafe or incorrect summary")
		}
	}
	paths, _ := filepath.Glob("data/research/universe-*.json")
	if len(paths) != 2 {
		t.Fatal("reports absent")
	}
	a, _ := os.ReadFile(paths[0])
	b, _ := os.ReadFile(paths[1])
	if !bytes.Equal(a, b) {
		t.Fatal("report hash changed on repeat")
	}
	var report universeOutput
	if json.Unmarshal(a, &report) != nil || len(report.Sources) != 3 || len(report.Universe.Days[0].Picks) != 30 || report.Universe.QualityPromoted || report.Universe.StrategyAdopted || report.DailySHA == "" {
		t.Fatal("provenance or flags lost")
	}
	catalog, _ := os.ReadFile(report.DailyCatalog)
	if hash(catalog) != report.DailySHA {
		t.Fatal("catalog hash mismatch")
	}
	files, _ := filepath.Glob("data/research/daily-catalog-*.json")
	if len(files) != 1 {
		t.Fatal("catalog not deduplicated")
	}
}

func TestUniverseExplicitHistoryManifestAndUnknownVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	m := universeFixture(t)
	m.Version = 2
	writeS01FixtureJSON(t, "inputs/manifest.json", m)
	if err := run(context.Background(), []string{"select-universe", "-input", "inputs/manifest.json"}, &bytes.Buffer{}); err != nil {
		t.Fatal("explicit daily history manifest", err)
	}
	m.Version = 3
	writeS01FixtureJSON(t, "inputs/manifest.json", m)
	if err := run(context.Background(), []string{"select-universe", "-input", "inputs/manifest.json"}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown daily history manifest admitted")
	}
}

func TestUniverseCLIRejectsTamperedSourcesCalendarAndRules(t *testing.T) {
	for _, fault := range []string{"source_bytes", "gzip_crc", "rule", "duplicate_calendar", "unknown_field"} {
		t.Run(fault, func(t *testing.T) {
			t.Chdir(t.TempDir())
			m := universeFixture(t)
			switch fault {
			case "source_bytes":
				_ = os.WriteFile("inputs/daily.csv.gz", []byte("private-marker"), 0600)
			case "gzip_crc":
				raw, _ := os.ReadFile("inputs/daily.csv.gz")
				raw[len(raw)-1] ^= 1
				_ = os.WriteFile("inputs/daily.csv.gz", raw, 0600)
				captureRaw, _ := os.ReadFile("inputs/daily.json")
				var capture jquantsbulk.Report
				_ = json.Unmarshal(captureRaw, &capture)
				capture.Files[0].SHA256 = hash(raw)
				captureRaw, _ = json.Marshal(capture)
				_ = os.WriteFile("inputs/daily.json", captureRaw, 0600)
			case "rule":
				m.RuleSHA = strings.Repeat("0", 64)
			case "duplicate_calendar":
				m.CalendarFiles = append(m.CalendarFiles, m.CalendarFiles[0])
				m.DailyFiles = append(m.DailyFiles, m.DailyFiles[0])
				m.MasterFiles = append(m.MasterFiles, m.MasterFiles[0])
			}
			raw, _ := json.Marshal(m)
			if fault == "unknown_field" {
				raw = append(raw[:len(raw)-1], []byte(`,"quality_override":true}`)...)
			}
			_ = os.WriteFile("inputs/manifest.json", raw, 0600)
			if err := run(context.Background(), []string{"select-universe", "-input", "inputs/manifest.json"}, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatal("invalid/private source admitted")
			}
		})
	}
	if err := run(context.Background(), []string{"select-universe", "-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
