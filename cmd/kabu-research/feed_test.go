package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/streamsignals"
)

func TestFeedDemoSavedEventsAndResultsReproduce(t *testing.T) {
	t.Chdir(t.TempDir())
	var previous []byte
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"feed-demo", "-direction", "short", "-variant", "B"}, &out); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob("data/research/feed-simulation-*.json")
	manifests, _ := filepath.Glob("data/research/research-run-*.json")
	if len(files) != 2 || len(manifests) != 2 {
		t.Fatal("missing artifacts")
	}
	for _, file := range files {
		raw, _ := os.ReadFile(file)
		if previous != nil && !bytes.Equal(raw, previous) {
			t.Fatal("non-deterministic results")
		}
		previous = raw
		var report streamsignals.DemoReport
		if json.Unmarshal(raw, &report) != nil || len(report.Cases) != 14 || report.ForwardTest || report.ExecutionIncluded {
			t.Fatal("scope")
		}
		for _, c := range report.Cases {
			if !c.Passed {
				t.Fatal(c.Name)
			}
		}
	}
	for _, file := range manifests {
		raw, _ := os.ReadFile(file)
		var m struct {
			InputFile  string `json:"input_file"`
			InputSHA   string `json:"input_sha256"`
			ReportFile string `json:"report_file"`
			ReportSHA  string `json:"report_sha256"`
		}
		if json.Unmarshal(raw, &m) != nil {
			t.Fatal("manifest")
		}
		input, err := os.ReadFile(filepath.Join("data/research", m.InputFile))
		if err != nil || hash(input) != m.InputSHA {
			t.Fatal("input provenance")
		}
		output, err := os.ReadFile(filepath.Join("data/research", m.ReportFile))
		if err != nil || hash(output) != m.ReportSHA {
			t.Fatal("output provenance")
		}
	}
}

func TestFeedDemoCannotReadExternalMarketFile(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, flag := range []string{"-input", "-reference", "-symbol", "-captured-at"} {
		if err := run(context.Background(), []string{"feed-demo", flag, "must-not-read"}, &bytes.Buffer{}); err == nil {
			t.Fatal("external input accepted")
		}
	}
	files, _ := filepath.Glob("data/research/*")
	if len(files) != 0 {
		t.Fatal("rejected run wrote files")
	}
}
