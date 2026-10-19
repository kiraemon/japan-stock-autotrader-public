package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStudyCannotOpenHoldoutOrUnpinnedInputs(t *testing.T) {
	for _, args := range [][]string{{"study-s01", "-phase", "holdout", "-input", "does-not-exist"}, {"study-s01"}, {"study-s01", "-phase", "all"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("unsafe phase or absent manifest accepted")
		}
	}
	t.Chdir(t.TempDir())
	raw, _ := json.Marshal(studyManifest{Version: 1, Preparation: "private-marker", PreparationSHA: strings.Repeat("0", 64)})
	if os.WriteFile("manifest.json", raw, 0600) != nil {
		t.Fatal("fixture write")
	}
	if err := run(context.Background(), []string{"study-s01", "-input", "manifest.json"}, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "private-marker") {
		t.Fatal("unpinned study admitted or path leaked")
	}
	if err := run(context.Background(), []string{"study-s01", "-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
func TestStudyFixedChronologicalPeriodsAndFormatting(t *testing.T) {
	raw, err := os.ReadFile("../../examples/research/long-history.plan.json")
	if err != nil {
		t.Fatal(err)
	}
	// Fabricated sessions with the required published boundaries/counts. This
	// tests partition routing only; the real CLI uses the pinned TSE calendar.
	sessions := []string{}
	for _, name := range []string{"train", "validation", "holdout"} {
		p := fixedPartitions[name]
		start, _ := time.Parse("2006-01-02", p.From)
		end, _ := time.Parse("2006-01-02", p.To)
		dates := []string{}
		for at := start; !at.After(end); at = at.AddDate(0, 0, 1) {
			if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
				dates = append(dates, at.Format("2006-01-02"))
			}
		}
		keep := append([]string{}, dates[:p.Sessions-1]...)
		keep = append(keep, p.To)
		sessions = append(sessions, keep...)
	}
	parts, err := studyPeriods(raw, sessions)
	if err != nil || len(parts["train"]) != 108 || len(parts["validation"]) != 36 || len(parts["holdout"]) != 36 || parts["validation"][35] != "2026-08-05" {
		t.Fatal("partition routing changed", err)
	}
	if _, err := studyPeriods(raw, sessions[:179]); err == nil {
		t.Fatal("missing study date accepted")
	}
	var changed map[string]any
	_ = json.Unmarshal(raw, &changed)
	changed["real_orders_enabled"] = true
	altered, _ := json.Marshal(changed)
	if _, err := studyPeriods(altered, sessions); err == nil {
		t.Fatal("order permission promoted")
	}
	if formatStudyMoney("1/3") != "0.33" || formatStudyMoney("invalid") != "unavailable" {
		t.Fatal("money summary format changed")
	}
}
