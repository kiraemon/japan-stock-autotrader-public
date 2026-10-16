package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuoteDemoBothDirectionsReproducesActualSavedInputs(t *testing.T) {
	for _, direction := range []string{"long", "short"} {
		t.Run(direction, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for i := 0; i < 2; i++ {
				if err := run(context.Background(), []string{"quote-demo", "-direction", direction}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
			}
			files, _ := filepath.Glob("data/research/quote-simulation-*.json")
			manifests, _ := filepath.Glob("data/research/research-run-*.json")
			if len(files) != 2 || len(manifests) != 2 {
				t.Fatal("artifacts")
			}
			left, _ := os.ReadFile(files[0])
			right, _ := os.ReadFile(files[1])
			if !bytes.Equal(left, right) {
				t.Fatal("result not deterministic")
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
					t.Fatal("input hash")
				}
				output, err := os.ReadFile(filepath.Join("data/research", m.ReportFile))
				if err != nil || hash(output) != m.ReportSHA {
					t.Fatal("output hash")
				}
			}
		})
	}
}

func TestQuoteAuditPreservesObservedStatusAndDoesNotExecute(t *testing.T) {
	t.Chdir(t.TempDir())
	at := time.Date(2026, 10, 16, 0, 16, 3, 0, time.UTC)
	board := syntheticBoard(at, 100)
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "environment": "test", "complete": true, "records": []any{map[string]any{"endpoint": "/board/0000@1", "received_at": at.Add(time.Second), "data": board}}})
	if os.WriteFile("snapshot.json", raw, 0600) != nil {
		t.Fatal("fixture")
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"quote-check", "-input", "snapshot.json", "-symbol", "0000@1", "-environment", "test"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fresh=false / execution_included=false") {
		t.Fatal("old data not marked stale")
	}
	files, _ := filepath.Glob("data/research/quote-audit-*.json")
	body, _ := os.ReadFile(files[0])
	var audit struct {
		Quote struct {
			Origin string `json:"origin"`
		} `json:"quote"`
		Execution bool `json:"execution_included"`
	}
	if json.Unmarshal(body, &audit) != nil || audit.Quote.Origin != "observed" || audit.Execution {
		t.Fatal("observed data promoted")
	}
	if err := run(context.Background(), []string{"quote-demo", "-input", "snapshot.json"}, &bytes.Buffer{}); err == nil {
		t.Fatal("external market file accepted by demo")
	}
}
