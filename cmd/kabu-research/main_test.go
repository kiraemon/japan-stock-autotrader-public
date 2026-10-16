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

func TestDemoAndSavedReplayAreIdenticalAndHashesResolve(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var output bytes.Buffer
			if err := run(context.Background(), []string{"demo", "-direction", side}, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "scope=synthetic_logic_test") || !strings.Contains(output.String(), "trades=1") {
				t.Fatal("demo scope missing")
			}
			inputs, _ := filepath.Glob("data/research/synthetic-bars-*.json")
			reports, _ := filepath.Glob("data/research/backtest-*.json")
			manifests, _ := filepath.Glob("data/research/research-run-*.json")
			if len(inputs) != 1 || len(reports) != 1 || len(manifests) != 1 {
				t.Fatal("missing artifacts")
			}
			raw, _ := os.ReadFile(manifests[0])
			var m struct {
				InputSHA    string          `json:"input_sha256"`
				InputFile   string          `json:"input_file"`
				ReportSHA   string          `json:"report_sha256"`
				SettingsSHA string          `json:"settings_sha256"`
				Settings    json.RawMessage `json:"settings"`
			}
			if json.Unmarshal(raw, &m) != nil {
				t.Fatal("manifest decode")
			}
			inputRaw, _ := os.ReadFile(inputs[0])
			original, _ := os.ReadFile(reports[0])
			if m.InputSHA != hash(inputRaw) || m.ReportSHA != hash(original) || m.InputFile != filepath.Base(inputs[0]) || m.SettingsSHA != hash(m.Settings) {
				t.Fatal("manifest cannot reproduce stored bytes")
			}
			if err := run(context.Background(), []string{"backtest", "-input", inputs[0], "-direction", side}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			reports, _ = filepath.Glob("data/research/backtest-*.json")
			if len(reports) != 2 {
				t.Fatal("report overwritten")
			}
			for _, file := range reports {
				raw, _ := os.ReadFile(file)
				if !bytes.Equal(raw, original) {
					t.Fatal("saved replay changed result")
				}
			}
		})
	}
}
func TestCSVConversionAndRealDataStayUnverified(t *testing.T) {
	t.Chdir(t.TempDir())
	const path = "synthetic-input.csv"
	if os.WriteFile(path, []byte("date,time,open,high,low,close,volume\n2026-10-14,09:00,100,101,99,100,100\n"), 0600) != nil {
		t.Fatal("write")
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"build-bars", "-format", "csv", "-input", path, "-symbol", "7203@1", "-csv-label", "start", "-captured-at", "2026-10-14T16:00:00+09:00"}, &out); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("data/research/bars-*.json")
	if len(files) != 1 {
		t.Fatal("bars missing")
	}
	manifests, _ := filepath.Glob("data/research/research-run-*.json")
	if len(manifests) != 1 {
		t.Fatal("conversion code provenance missing")
	}
	manifestRaw, _ := os.ReadFile(manifests[0])
	var manifest struct {
		InputSHA  string `json:"input_sha256"`
		ReportSHA string `json:"report_sha256"`
	}
	if json.Unmarshal(manifestRaw, &manifest) != nil {
		t.Fatal("conversion manifest decode")
	}
	sourceRaw, _ := os.ReadFile(path)
	barsRaw, _ := os.ReadFile(files[0])
	if manifest.InputSHA != hash(sourceRaw) || manifest.ReportSHA != hash(barsRaw) {
		t.Fatal("conversion cannot be traced to exact source/output bytes")
	}
	out.Reset()
	if err := run(context.Background(), []string{"backtest", "-input", files[0]}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "trades=0 / unevaluable=1") {
		t.Fatal("unverified source produced trades")
	}
}
func TestInvalidCommandsCancellationAndSecretInputDoNotLeak(t *testing.T) {
	t.Chdir(t.TempDir())
	if os.WriteFile("not-bars.json", []byte(`{"password":"synthetic-secret-do-not-log"}`), 0600) != nil {
		t.Fatal("write")
	}
	for _, args := range [][]string{{"wrong"}, {"demo", "-direction", "wrong"}, {"demo", "-input", "not-bars.json"}, {"backtest", "-input", "not-bars.json"}, {"build-bars", "-format", "csv", "-input", "not-bars.json"}} {
		var out bytes.Buffer
		err := run(context.Background(), args, &out)
		if err == nil || strings.Contains(out.String()+err.Error(), "synthetic-secret-do-not-log") {
			t.Fatal("invalid input accepted or secret leaked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if run(ctx, []string{"demo"}, &bytes.Buffer{}) == nil {
		t.Fatal("cancel ignored")
	}
	if run(context.Background(), []string{"-h"}, &bytes.Buffer{}) != nil {
		t.Fatal("help failed")
	}
}
