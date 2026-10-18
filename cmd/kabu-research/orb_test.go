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
)

func writeORBFixture(t *testing.T) orbManifest {
	t.Helper()
	date := "2026-06-22"
	m := orbManifest{Version: 1, Environment: "test", Date: date, CSVLabel: "start", CalendarSource: "synthetic_fixture", Previous: []string{}, Sources: []orbSource{{Symbol: "0000@1", CSV: "chart.csv", CapturedAt: "2026-06-22T16:00:00+09:00"}}}
	at, _ := time.Parse("2006-01-02", date)
	for at = at.AddDate(0, 0, -1); len(m.Previous) < 14; at = at.AddDate(0, 0, -1) {
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			m.Previous = append([]string{at.Format("2006-01-02")}, m.Previous...)
		}
	}
	var csv strings.Builder
	csv.WriteString("日付,始値,高値,安値,終値,売買高\n")
	for _, day := range append(append([]string{}, m.Previous...), date) {
		for minute := 0; minute < 5; minute++ {
			fmt.Fprintf(&csv, "%s 09:%02d:00,100,102,99,101,10\n", strings.ReplaceAll(day, "-", "/"), minute)
		}
	}
	if os.MkdirAll("inputs", 0700) != nil || os.WriteFile("inputs/chart.csv", []byte(csv.String()), 0600) != nil {
		t.Fatal("fixture write")
	}
	raw, _ := json.Marshal(m)
	if os.WriteFile("inputs/orb.json", raw, 0600) != nil {
		t.Fatal("manifest write")
	}
	return m
}

func TestORBInputProvenanceReproducibilityAndSafeOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	m := writeORBFixture(t)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"orb-input-check", "-input", "inputs/orb.json"}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "previous_complete=14/14") || !strings.Contains(out.String(), "calculation_available=true") || strings.Contains(out.String(), "volume_shares") || strings.Contains(out.String(), "range_scaled") {
			t.Fatal("wrong CLI summary or price disclosure")
		}
	}
	paths, _ := filepath.Glob("data/research/orb-input-*.json")
	if len(paths) != 2 {
		t.Fatal("reports missing")
	}
	a, _ := os.ReadFile(paths[0])
	b, _ := os.ReadFile(paths[1])
	if !bytes.Equal(a, b) {
		t.Fatal("non-deterministic report")
	}
	var r orbReport
	if json.Unmarshal(a, &r) != nil {
		t.Fatal("report decode")
	}
	input, _ := os.ReadFile("inputs/orb.json")
	csv, _ := os.ReadFile("inputs/chart.csv")
	if r.ManifestSHA256 != hash(input) || len(r.Sources) != 1 || r.Sources[0].CSVSHA256 != hash(csv) || r.CalendarAutomaticallyVerified || r.Inputs[0].QualityPromoted || r.Inputs[0].StrategyAdopted || *r.Inputs[0].RelativeVolume != "1" {
		t.Fatal("provenance or scope")
	}
	if len(r.Inputs[0].Target.SourceIssues) == 0 {
		t.Fatal("converter quality issues lost")
	}
	// Explicitly absent day remains in the denominator window, with no ratio.
	m.Previous[0] = "2026-06-01"
	raw, _ := json.Marshal(m)
	_ = os.WriteFile("inputs/orb.json", raw, 0600)
	var out bytes.Buffer
	if err := run(context.Background(), []string{"orb-input-check", "-input", "inputs/orb.json"}, &out); err != nil || !strings.Contains(out.String(), "previous_complete=13/14") || !strings.Contains(out.String(), "calculation_available=false") {
		t.Fatal("absent reference admitted", err)
	}
}

func TestORBManifestAndArgumentsRejectInvalidInput(t *testing.T) {
	t.Chdir(t.TempDir())
	m := writeORBFixture(t)
	raw, _ := json.Marshal(m)
	for _, value := range [][]byte{append(raw, []byte(" {}")...), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"quality_override":true`), 1), bytes.Replace(raw, []byte(`"environment":"test"`), []byte(`"environment":"secret-marker"`), 1)} {
		if _, err := decodeORB(value); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("bad manifest or error disclosure")
		}
	}
	m.Sources = append(m.Sources, m.Sources[0])
	value, _ := json.Marshal(m)
	if _, err := decodeORB(value); err == nil {
		t.Fatal("duplicate symbol admitted")
	}
	if err := run(context.Background(), []string{"orb-input-check", "-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"orb-input-check"}, {"orb-input-check", "-input", "inputs/orb.json", "-direction", "long"}, {"orb-input-check", "-input", "absent.json"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("bad arguments admitted")
		}
	}
}
