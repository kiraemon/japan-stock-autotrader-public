package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestRound2PeriodsRejectOldMissingReorderedAndRetunedSessions(t *testing.T) {
	raw, err := os.ReadFile("../../examples/research/s01-round2-periods.plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var p round2PeriodPlan
	if json.Unmarshal(raw, &p) != nil {
		t.Fatal("period fixture")
	}
	sessions := []string{}
	for _, name := range []string{"train", "validation", "holdout"} {
		sessions = append(sessions, p.Parts[name].Dates...)
	}
	parts, err := round2Periods(raw, sessions)
	if err != nil || len(parts["train"]) != 99 || len(parts["validation"]) != 33 || len(parts["holdout"]) != 34 {
		t.Fatal("frozen periods changed")
	}
	for _, fault := range []string{"missing", "reorder", "known_2026", "plan"} {
		r, dates := append([]byte{}, raw...), append([]string{}, sessions...)
		switch fault {
		case "missing":
			dates = dates[:165]
		case "reorder":
			dates[0], dates[1] = dates[1], dates[0]
		case "known_2026":
			dates[0] = "2026-01-05"
		case "plan":
			r = append(r, ' ')
		}
		if _, err := round2Periods(r, dates); err == nil {
			t.Fatalf("allowed %s", fault)
		}
	}
	parts["holdout"][0] = "changed"
	if sessions[132] != "2025-09-11" {
		t.Fatal("period ownership")
	}
	all := append(append([]string{}, p.Warmup...), sessions...)
	sources := []universeSource{}
	for _, pin := range p.Pins {
		sources = append(sources, universeSource{Kind: "calendar", Capture: pin.Path, CaptureSHA: pin.SHA})
	}
	if err := round2CalendarPins(raw, sources, all); err != nil {
		t.Fatal(err)
	}
	sources[0].CaptureSHA = "tampered"
	if round2CalendarPins(raw, sources, all) == nil || round2CalendarPins(raw, sources[1:], all) == nil {
		t.Fatal("calendar pins not bound")
	}
}

func TestRound2PreparationCannotConsumeLegacyManifestOrPublishPnL(t *testing.T) {
	plan, err := os.ReadFile("../../examples/research/s01-jp20.plan.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	s01Fixture(t, plan)
	var out bytes.Buffer
	if err := run(context.Background(), []string{"prepare-s01-round2", "-input", "inputs/s01.json"}, &out); err == nil {
		t.Fatal("old preparation used as new research")
	}
	if err := run(context.Background(), []string{"prepare-s01-round2", "-h"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("data/research/s01-r2-input-index.json"); !os.IsNotExist(err) {
		t.Fatal("invalid input published")
	}
}
