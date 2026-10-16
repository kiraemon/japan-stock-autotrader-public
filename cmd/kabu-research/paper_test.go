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

func TestPaperDemoIsSyntheticReproducibleAndTracksActualEventBytes(t *testing.T) {
	t.Chdir(t.TempDir())
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"paper-demo"}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "cases=9 / passed=9 / forward_test=false / live_submission_capability=false") {
			t.Fatal("synthetic scope/results lost")
		}
	}
	reports, _ := filepath.Glob("data/research/paper-simulation-*.json")
	if len(reports) != 2 {
		t.Fatal("paper simulation report overwritten")
	}
	a, _ := os.ReadFile(reports[0])
	b, _ := os.ReadFile(reports[1])
	if !bytes.Equal(a, b) {
		t.Fatal("same synthetic suite changed report")
	}
	manifests, _ := filepath.Glob("data/research/research-run-*.json")
	for _, path := range manifests {
		raw, _ := os.ReadFile(path)
		var m struct {
			InputFile string `json:"input_file"`
			InputSHA  string `json:"input_sha256"`
			ReportSHA string `json:"report_sha256"`
		}
		if json.Unmarshal(raw, &m) != nil {
			t.Fatal("manifest decode")
		}
		input, _ := os.ReadFile(filepath.Join("data/research", m.InputFile))
		if len(input) == 0 || m.InputSHA != hash(input) || m.ReportSHA != hash(a) {
			t.Fatal("event fixture bytes are not reproducible from manifest")
		}
	}
	if err := run(context.Background(), []string{"paper-demo", "-input", "must-not-read-real-file.json"}, &bytes.Buffer{}); err == nil {
		t.Fatal("paper demo accepted external data input")
	}
}
