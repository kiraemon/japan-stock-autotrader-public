package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/papertrade"
)

func TestPaperReplayCLIProvenanceAndScope(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		for _, variant := range []string{"A", "B"} {
			t.Run(side+variant, func(t *testing.T) {
				t.Chdir(t.TempDir())
				if err := run(context.Background(), []string{"paper-replay", "-direction", side, "-variant", variant}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				files, _ := filepath.Glob("data/research/paper-replay-*.json")
				manifests, _ := filepath.Glob("data/research/research-run-*.json")
				if len(files) != 1 || len(manifests) != 1 {
					t.Fatal("missing artifacts")
				}
				raw, _ := os.ReadFile(files[0])
				var reports []papertrade.ReplayReport
				if json.Unmarshal(raw, &reports) != nil || len(reports) != 4 {
					t.Fatal("missing fill models")
				}
				for _, report := range reports {
					if report.ForwardTest || report.LiveSubmissionCapability {
						t.Fatal("incorrect scope")
					}
					if variant == "B" && len(report.Days[0].Final.Orders) != 0 {
						t.Fatal("filtered fixture created orders")
					}
				}
				manifestRaw, _ := os.ReadFile(manifests[0])
				var m struct {
					InputFile   string          `json:"input_file"`
					InputSHA    string          `json:"input_sha256"`
					ReportSHA   string          `json:"report_sha256"`
					Settings    json.RawMessage `json:"settings"`
					SettingsSHA string          `json:"settings_sha256"`
				}
				if json.Unmarshal(manifestRaw, &m) != nil {
					t.Fatal("manifest")
				}
				inputRaw, err := os.ReadFile(filepath.Join("data/research", m.InputFile))
				if err != nil || hash(inputRaw) != m.InputSHA || hash(raw) != m.ReportSHA || hash(m.Settings) != m.SettingsSHA {
					t.Fatal("provenance mismatch")
				}
			})
		}
	}
}

func TestPaperReplayCLIRejectsExternalFilesBeforeOpening(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, flag := range []string{"-input", "-reference", "-symbol", "-captured-at"} {
		if err := run(context.Background(), []string{"paper-replay", flag, "must-not-read"}, &bytes.Buffer{}); err == nil {
			t.Fatal("external input accepted")
		}
	}
	files, _ := filepath.Glob("data/research/*")
	if len(files) != 0 {
		t.Fatal("rejected run wrote files")
	}
}
