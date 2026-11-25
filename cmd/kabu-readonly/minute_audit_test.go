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

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/capturehistory"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/kabu"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/telemetry"
)

type minuteAuditSession struct {
	cliAlignedSession
	fault string
}

func (s *minuteAuditSession) TimeAndSales(ctx context.Context, symbol string) (json.RawMessage, error) {
	s.histories++
	s.clock.at = s.clock.at.Add(250 * time.Millisecond)
	start := marketdata.Clock(marketdata.Date(s.clock.at), 9, 0)
	rows := []any{}
	for at := start; at.Before(s.clock.at.Truncate(time.Minute)); at = at.Add(time.Minute) {
		if s.fault == "missing" && at.Equal(start.Add(time.Minute)) {
			continue
		}
		price := 100
		if s.fault == "revised" && s.snapshots > 0 && at.Equal(start) {
			price = 101
		}
		rows = append(rows, map[string]any{"Time": at.Add(20 * time.Second), "Price": price, "Volume": 10})
	}
	if s.fault == "future" {
		rows = append(rows, map[string]any{"Time": s.clock.at.Add(500 * time.Millisecond), "Price": 100, "Volume": 10})
	}
	raw, _ := json.Marshal(map[string]any{"Symbol": strings.Split(symbol, "@")[0], "Exchange": 1, "TradingPriceCount": len(rows), "TradingPrice": rows})
	return raw, ctx.Err()
}

func auditReports(t *testing.T) []minuteAuditReport {
	t.Helper()
	files, _ := filepath.Glob("data/history/test/minute-audit-*.json")
	reports := []minuteAuditReport{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		var report minuteAuditReport
		if err != nil || json.Unmarshal(raw, &report) != nil {
			t.Fatal("audit report")
		}
		reports = append(reports, report)
	}
	return reports
}

func TestMinuteAuditCLIProcesses111RoundsWithActualInjectedClock(t *testing.T) {
	t.Chdir(t.TempDir())
	c, args := cliAlignedFixture()
	client := &minuteAuditSession{cliAlignedSession: cliAlignedSession{clock: c}}
	var out bytes.Buffer
	err := runWithSessionClock(context.Background(), append(args, "-history-minute-audit"), strings.NewReader("synthetic-only\n"), &out, func(kabu.Environment) (session, error) { return client, nil }, c)
	if err != nil || client.auths != 1 || client.histories != 333 || client.snapshots != 111 || !client.closed {
		t.Fatal("pipeline", err, client.histories, client.snapshots)
	}
	reports := auditReports(t)
	if len(reports) != 111 {
		t.Fatal("audit count", len(reports))
	}
	for _, report := range reports {
		if report.ErrorCode != "" || len(report.Frames) != 3 || report.ClockModel != "processing_clock_not_archive_replay" || report.ForwardTest || report.ExecutionIncluded || report.StrategyEligible {
			t.Fatal("audit contract", report)
		}
		for _, frame := range report.Frames {
			if !frame.Outcome.Accepted || len(frame.Outcome.Candidates) != 1 || frame.State.LiveReady || frame.ObservedAt.Before(frame.Capture.SavedAt) || frame.ObservedAt.After(report.Deadline) {
				t.Fatal("processing time lost", frame)
			}
			candidate := frame.Outcome.Candidates[0]
			if candidate.StrategyEligible || candidate.Late || !candidate.ObservedAt.Equal(frame.ObservedAt) || candidate.SourceSHA256 != frame.Capture.SHA256 || !strings.Contains(strings.Join(candidate.Issues, ","), "source_validation_pending") {
				t.Fatal("quality promoted", candidate)
			}
		}
	}
	files, _ := filepath.Glob("data/history/test/aligned-*.json")
	for _, path := range files {
		raw, _ := os.ReadFile(path)
		var r capturehistory.AlignedRound
		if json.Unmarshal(raw, &r) != nil || !r.ConversionIncluded || !r.ProcessingCompleted || !r.ProcessingOnTime || r.ProcessingReport == "" || r.ProcessingCompletedAt == nil {
			t.Fatal("processing provenance")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), r.ProcessingReport)); err != nil {
			t.Fatal("missing audit reference")
		}
	}
	if !strings.Contains(out.String(), "conversion_included=true / strategy_eligible=false") {
		t.Fatal("missing mode boundary")
	}
	t.Log("synthetic_fixture=true / authenticated_sessions=1 / rounds=111 / raw_archives=333 / minute_candidates=333 / audit_reports=111 / ordinary_snapshots=111 / strategy_eligible=false")
}

func TestMinuteAuditCLIFaultsRetainEvidenceAndStop(t *testing.T) {
	for _, tc := range []struct {
		fault, reason     string
		normal, histories int
	}{{"future", "history_market_time_in_future", 0, 3}, {"revised", "candidate_minute_revised", 1, 6}, {"missing", "closed_minute_missing", 1, 6}} {
		t.Run(tc.fault, func(t *testing.T) {
			t.Chdir(t.TempDir())
			c, args := cliAlignedFixture()
			client := &minuteAuditSession{cliAlignedSession: cliAlignedSession{clock: c}, fault: tc.fault}
			err := runWithSessionClock(context.Background(), append(args, "-history-minute-audit"), strings.NewReader("synthetic-only\n"), &bytes.Buffer{}, func(kabu.Environment) (session, error) { return client, nil }, c)
			if err == nil || !strings.Contains(err.Error(), tc.reason) || client.snapshots != tc.normal || client.histories != tc.histories || !client.closed {
				t.Fatal("fault did not stop", err, client.histories, client.snapshots)
			}
			found := false
			for _, r := range auditReports(t) {
				found = found || r.ErrorCode == tc.reason
			}
			status := telemetry.Observe("data", "test", time.Now())
			if !found || status.State != "failed" {
				t.Fatal("failure evidence lost")
			}
			t.Log("synthetic_fixture=true / stop_reason=" + tc.reason + " / retry=false / strategy_eligible=false")
		})
	}
}

func TestMinuteAuditRejectsWithoutScheduleBeforeCredentials(t *testing.T) {
	err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-history-minute-audit", "-onepassword"}, unreadableInput{t}, &bytes.Buffer{}, func(kabu.Environment) (session, error) { t.Fatal("opened before validation"); return nil, nil })
	if err == nil {
		t.Fatal("missing schedule accepted")
	}
}

func TestMinuteAuditRejectsChangedOriginalAndWillNotResume(t *testing.T) {
	dir := t.TempDir()
	date := "2026-10-19"
	now := marketdata.Clock(date, 9, 1).Add(3 * time.Second)
	audit, err := newMinuteAudit(date, "test", []string{"7203@1"}, dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "history-fixture.json"), []byte("changed synthetic fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	round := capturehistory.AlignedRound{Date: date, Environment: kabu.Test, MinuteStart: marketdata.Clock(date, 9, 0), Deadline: now.Add(7 * time.Second), RawCompletedAt: now, RawCompleted: true, RawOnTime: true, Captures: []capturehistory.CaptureTiming{{Symbol: "7203@1", File: "history-fixture.json", SHA256: strings.Repeat("0", 64), Complete: true}}}
	path, err := audit(context.Background(), round)
	if err == nil || path == "" || !strings.Contains(err.Error(), "source_hash_or_size_changed") {
		t.Fatal("modified source accepted", err)
	}
	if path, err = audit(context.Background(), round); err == nil || path != "" {
		t.Fatal("halted audit resumed")
	}
}
