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

	"github.com/kiraemon/japan-stock-autotrader/internal/features/streambars"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/streamsignals"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func historyFeedFixture(t *testing.T, path string, received time.Time) []byte {
	t.Helper()
	rows := []any{}
	for i := 0; i < 2; i++ {
		rows = append(rows, map[string]any{"Time": marketdata.Clock("2026-10-16", 9, i), "Price": 100, "Volume": 10})
	}
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "environment": "production", "symbol": "7203@1", "complete": true, "started_at": received, "history_received_at": received, "finished_at": received, "time_and_sales": map[string]any{"Symbol": "7203", "Exchange": 1, "TradingPriceCount": 2, "TradingPrice": rows}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHistoryFeedCLIReplaysRawResponsesWithoutPromotion(t *testing.T) {
	t.Chdir(t.TempDir())
	at := marketdata.Clock("2026-10-16", 9, 1).Add(streambars.CloseLag)
	first := historyFeedFixture(t, "first.json", at)
	second := historyFeedFixture(t, "second.json", at.Add(time.Minute))
	args := []string{"history-feed-check", "-input", "first.json", "-reference", "second.json", "-symbol", "7203@1"}
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "candidates=2 / late=0 / halted=false") || !strings.Contains(out.String(), "forward_test=false / live_ready=false / execution_included=false") {
			t.Fatal("audit scope", out.String())
		}
	}
	files, _ := filepath.Glob("data/research/history-minute-audit-*.json")
	if len(files) != 2 {
		t.Fatal("report count")
	}
	a, _ := os.ReadFile(files[0])
	b, _ := os.ReadFile(files[1])
	if !bytes.Equal(a, b) {
		t.Fatal("audit replay not deterministic")
	}
	var report struct {
		ClockModel string `json:"clock_model"`
		Frames     []struct {
			SHA256  string             `json:"source_sha256"`
			Outcome streambars.Outcome `json:"outcome"`
		} `json:"frames"`
		Final streambars.State `json:"final"`
	}
	if json.Unmarshal(a, &report) != nil || report.ClockModel != "archive_finished_at_replay_not_live" || report.Final.LiveReady || report.Frames[0].SHA256 != hash(first) || report.Frames[1].SHA256 != hash(second) || report.Frames[1].Outcome.Duplicates != 1 {
		t.Fatal("provenance or overlap")
	}
	c := report.Frames[0].Outcome.Candidates[0]
	if c.StrategyEligible || *c.Bar.Volume != 10 || len(c.Issues) < 5 {
		t.Fatal("quality cleared or quantity duplicated")
	}
	feed, err := streamsignals.New(streamsignals.Config{Date: "2026-10-16", Symbol: "7203@1", Origin: "observed", Direction: r001.Long, Variant: r001.A})
	if err != nil {
		t.Fatal(err)
	}
	if rejected := feed.Offer(at, streamsignals.Observation{ReceivedAt: at, Bar: c.Bar}); rejected.Accepted || rejected.Reason != "observed_source_validation_pending" {
		t.Fatal("observed strategy boundary relaxed", rejected)
	}
	manifests, _ := filepath.Glob("data/research/research-run-*.json")
	if len(manifests) != 2 {
		t.Fatal("manifest count")
	}
	for _, path := range manifests {
		raw, _ := os.ReadFile(path)
		var m struct {
			InputSHA  string `json:"input_sha256"`
			ReportSHA string `json:"report_sha256"`
			Settings  struct {
				ReferenceSHA256 string
			} `json:"settings"`
		}
		if json.Unmarshal(raw, &m) != nil || m.InputSHA != hash(first) || m.ReportSHA != hash(a) || m.Settings.ReferenceSHA256 != hash(second) {
			t.Fatal("input/output/reference hash")
		}
	}
	for path, original := range map[string][]byte{"first.json": first, "second.json": second} {
		raw, _ := os.ReadFile(path)
		if !bytes.Equal(raw, original) {
			t.Fatal("source overwritten")
		}
	}
}

func TestHistoryFeedCLIRefusesReorderingAndClockOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	at := marketdata.Clock("2026-10-16", 9, 1).Add(streambars.CloseLag)
	historyFeedFixture(t, "early.json", at)
	historyFeedFixture(t, "late.json", at.Add(time.Minute))
	var out bytes.Buffer
	args := []string{"history-feed-check", "-input", "late.json", "-reference", "early.json", "-symbol", "7203@1"}
	if err := run(context.Background(), args, &out); err != nil || !strings.Contains(out.String(), "halted=true / reason=observation_clock_reversed_or_invalid") {
		t.Fatal("frames silently sorted", out.String(), err)
	}
	for _, args := range [][]string{
		{"history-feed-check"},
		{"history-feed-check", "-input", "early.json", "-reference", "late.json", "-symbol", "7203@1", "-captured-at", at.Format(time.RFC3339)},
		{"history-feed-check", "-input", "early.json", "-reference", "late.json", "-symbol", "7203@1", "-csv-label", "start"},
	} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid/override settings accepted")
		}
	}
}
