package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayDefaultFixtureAndSavedInputHaveIdenticalSignals(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, variant := range []string{"A", "B"} {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"replay", "-direction", "short", "-variant", variant}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "observed_closed_minutes=111 / signal_candidates=1") || !strings.Contains(out.String(), "forward_test=false / execution_included=false") {
			t.Fatal("replay scope lost")
		}
		if variant == "B" && !strings.Contains(out.String(), "accepted=false / reason=volume_below_1_5x_previous_5") {
			t.Fatal("B fixture rejection hidden")
		}
	}
	inputs, _ := filepath.Glob("data/research/synthetic-bars-*.json")
	if len(inputs) != 2 {
		t.Fatal("fixture source not saved")
	}
	before, _ := filepath.Glob("data/research/signal-replay-*.json")
	for _, input := range inputs {
		if err := run(context.Background(), []string{"replay", "-input", input, "-direction", "short", "-variant", "A"}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := filepath.Glob("data/research/signal-replay-*.json")
	if len(before) != 2 || len(after) != 4 {
		t.Fatal("replay overwrote earlier output")
	}
	var originalA []byte
	aCount := 0
	for _, path := range after {
		raw, _ := os.ReadFile(path)
		var report struct {
			Rule string `json:"strategy_version"`
		}
		if json.Unmarshal(raw, &report) != nil {
			t.Fatal("replay decode")
		}
		if strings.HasSuffix(report.Rule, "-A") {
			aCount++
			if originalA == nil {
				originalA = raw
			} else if !bytes.Equal(originalA, raw) {
				t.Fatal("same saved fixture changed replay events")
			}
		}
	}
	if aCount != 3 {
		t.Fatal("expected original A and two saved-source A replays")
	}
	manifests, _ := filepath.Glob("data/research/research-run-*.json")
	for _, path := range manifests {
		raw, _ := os.ReadFile(path)
		var m struct {
			InputFile string `json:"input_file"`
			InputSHA  string `json:"input_sha256"`
		}
		if json.Unmarshal(raw, &m) != nil {
			t.Fatal("manifest decode")
		}
		input, _ := os.ReadFile(filepath.Join("data/research", m.InputFile))
		if m.InputSHA != hash(input) {
			t.Fatal("input hash cannot resolve original saved bytes")
		}
	}
}
