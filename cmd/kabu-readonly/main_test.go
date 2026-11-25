package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/instanceguard"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/kabu"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/telemetry"
)

func TestInvalidArgumentsFailBeforeAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-symbols", "7203@1"},
		{"-symbols", "7203@9", "-password-stdin"},
		{"-symbols", "7203@1", "-environment", "other", "-password-stdin"},
		{"-symbols", "7203@1", "-password-stdin", "unexpected"},
		{"-symbols", "7203@1", "-count", "0"},
		{"-symbols", "7203@1", "-count", "10001"},
		{"-symbols", "7203@1", "-interval", "0s"},
		{"-symbols", "7203@1", "-interval", "2h"},
		{"-symbols", "7203@1", "-onepassword", "-password-stdin"},
	} {
		var output bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader("test-only-password\n"), &output); err == nil {
			t.Fatalf("expected validation failure for %v", args)
		}
		if strings.Contains(output.String(), "test-only-password") {
			t.Fatal("credential leaked")
		}
	}
}

type unreadableInput struct{ t *testing.T }

func (r unreadableInput) Read([]byte) (int, error) {
	r.t.Fatal("password was read before acquiring the session")
	return 0, nil
}

func TestSecondInstanceRejectedBeforePasswordRead(t *testing.T) {
	var output bytes.Buffer
	err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-password-stdin"}, unreadableInput{t}, &output,
		func(kabu.Environment) (session, error) { return nil, instanceguard.ErrHeld })
	if !errors.Is(err, instanceguard.ErrHeld) {
		t.Fatal(err)
	}
}

func TestSecondInstanceRejectedBeforeOnePasswordConfiguration(t *testing.T) {
	var output bytes.Buffer
	err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-onepassword"}, unreadableInput{t}, &output,
		func(kabu.Environment) (session, error) { return nil, instanceguard.ErrHeld })
	if !errors.Is(err, instanceguard.ErrHeld) {
		t.Fatal("credential resolution preceded the startup gate")
	}
}

type observedSession struct {
	failAuth bool
	failRead bool
	waitRead bool
	closed   bool
}

func (s *observedSession) Authenticate(context.Context, string) error {
	if s.failAuth {
		return errors.New("synthetic auth failure")
	}
	return nil
}
func (s *observedSession) Close() { s.closed = true }
func (s *observedSession) Cash(ctx context.Context) (json.RawMessage, error) {
	if s.waitRead {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.failRead {
		return nil, errors.New("synthetic read failure")
	}
	return json.RawMessage(`{}`), nil
}
func (s *observedSession) Margin(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (s *observedSession) Positions(context.Context, bool) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (s *observedSession) Board(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"CurrentPrice":null}`), nil
}

func TestSessionLifecycleMetadata(t *testing.T) {
	for _, tc := range []struct {
		name               string
		authFail, readFail bool
		state              string
		saved              int
	}{{"complete", false, false, "completed", 1}, {"auth failure", true, false, "failed", 0}, {"partial", false, true, "failed", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			client := &observedSession{failAuth: tc.authFail, failRead: tc.readFail}
			err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-password-stdin"}, strings.NewReader("synthetic-password\n"), &bytes.Buffer{}, func(kabu.Environment) (session, error) { return client, nil })
			if (err != nil) != (tc.state == "failed") || !client.closed {
				t.Fatal("lifecycle failed")
			}
			status := telemetry.Observe("data", "test", time.Now())
			if status.State != tc.state || status.Saved != tc.saved {
				t.Fatalf("state=%s saved=%d", status.State, status.Saved)
			}
		})
	}
}

func TestPasswordWaitCancellationReleasesSession(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	client := &observedSession{}
	done := make(chan error, 1)
	go func() {
		done <- runWithSession(ctx, []string{"-symbols", "7203@1", "-password-stdin"}, reader, &bytes.Buffer{}, func(kabu.Environment) (session, error) { return client, nil })
	}()
	deadline := time.Now().Add(2 * time.Second)
	for telemetry.Observe("data", "test", time.Now()).Phase != "credentials" {
		if time.Now().After(deadline) {
			t.Fatal("not waiting for input")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !client.closed {
			t.Fatal("cancellation not closed")
		}
	case <-time.After(time.Second):
		t.Fatal("input wait blocked cancellation")
	}
	if observed := telemetry.Observe("data", "test", time.Now()); observed.State != "interrupted" {
		t.Fatalf("interruption not recorded: state=%s phase=%s", observed.State, observed.Phase)
	}
}

func TestEmptyClosedInputIsInterruptedStart(t *testing.T) {
	t.Chdir(t.TempDir())
	client := &observedSession{}
	err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-password-stdin"}, strings.NewReader(""), &bytes.Buffer{}, func(kabu.Environment) (session, error) { return client, nil })
	if !errors.Is(err, errInputClosed) || !client.closed || telemetry.Observe("data", "test", time.Now()).State != "interrupted" {
		t.Fatal("closed console input not classified as interrupted")
	}
}

func TestInvalidEndRejectedBeforeSessionOrPassword(t *testing.T) {
	for _, value := range []string{"15:35", "2026-10-16T15:35:00", time.Now().Add(-time.Hour).Format(time.RFC3339), time.Now().Add(25 * time.Hour).Format(time.RFC3339)} {
		err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-password-stdin", "-until", value}, unreadableInput{t}, &bytes.Buffer{}, func(kabu.Environment) (session, error) {
			t.Fatal("session opened with invalid end time")
			return nil, nil
		})
		if err == nil {
			t.Fatal("invalid end accepted")
		}
	}
}

func TestScheduledCLILifecycleAndCountLimit(t *testing.T) {
	for _, explicitCount := range []bool{false, true} {
		t.Run(map[bool]string{false: "until", true: "count first"}[explicitCount], func(t *testing.T) {
			t.Chdir(t.TempDir())
			client := &observedSession{}
			until := time.Now().Add(2 * time.Second)
			args := []string{"-symbols", "7203@1", "-password-stdin", "-until", until.Format(time.RFC3339Nano), "-interval", "1h"}
			planned := 10000
			if explicitCount {
				args = append(args, "-count", "1")
				planned = 1
			}
			var output bytes.Buffer
			err := runWithSession(context.Background(), args, strings.NewReader("synthetic-only\n"), &output, func(kabu.Environment) (session, error) { return client, nil })
			status := telemetry.Observe("data", "test", time.Now())
			wantState := "scheduled_end"
			if explicitCount {
				wantState = "completed"
			}
			if err != nil || !client.closed || status.State != wantState || status.Saved != 1 || status.Planned != planned {
				t.Fatalf("state=%s saved=%d planned=%d err=%v", status.State, status.Saved, status.Planned, err)
			}
			if strings.Contains(output.String(), "終了日時に到達") == explicitCount {
				t.Fatal("stop reason incorrect")
			}
			t.Log(output.String())
			t.Logf("state=%s / saved=%d / maximum=%d / session_closed=%t", status.State, status.Saved, status.Planned, client.closed)
		})
	}
}

func TestScheduledReadSavesPartialAndClosesSession(t *testing.T) {
	t.Chdir(t.TempDir())
	client := &observedSession{waitRead: true}
	var output bytes.Buffer
	err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-password-stdin", "-until", time.Now().Add(time.Second).Format(time.RFC3339Nano)}, strings.NewReader("synthetic-only\n"), &output, func(kabu.Environment) (session, error) { return client, nil })
	status := telemetry.Observe("data", "test", time.Now())
	if err != nil || !client.closed || status.State != "scheduled_end" || status.Saved != 1 || !strings.Contains(output.String(), "complete=false / records=1") {
		t.Fatalf("partial stop failed: state=%s saved=%d err=%v", status.State, status.Saved, err)
	}
	t.Log(output.String())
	t.Logf("state=%s / saved=%d / session_closed=%t", status.State, status.Saved, client.closed)
}

type historySession struct {
	observedSession
	historyCalls   int
	historyFailure bool
}

func (s *historySession) TimeAndSales(context.Context, string) (json.RawMessage, error) {
	s.historyCalls++
	if s.historyFailure {
		return nil, errors.New("synthetic history failure")
	}
	return json.RawMessage(`{"Symbol":"7203","Exchange":1,"TradingPriceCount":0,"TradingPrice":[]}`), nil
}
func (s *historySession) Board(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"Symbol":"7203","Exchange":1,"TradingVolume":0}`), nil
}

func TestHistoryBootstrapUsesSameSessionAndArchivesSeparately(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			t.Chdir(t.TempDir())
			client := &historySession{historyFailure: failure}
			opens := 0
			err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-history-symbols", "7203@1", "-password-stdin"}, strings.NewReader("synthetic-only\n"), &bytes.Buffer{}, func(kabu.Environment) (session, error) { opens++; return client, nil })
			files, _ := filepath.Glob("data/history/test/history-*.json")
			snapshots, _ := filepath.Glob("data/test/snapshot-*.json")
			if (err != nil) != failure || opens != 1 || client.historyCalls != 1 || !client.closed || len(files) != 1 || (len(snapshots) == 0) != failure {
				t.Fatal("history/session pipeline failed")
			}
			status := telemetry.Observe("data", "test", time.Now())
			want := "completed"
			if failure {
				want = "failed"
			}
			if status.State != want {
				t.Fatalf("state=%s", status.State)
			}
			raw, e := os.ReadFile(files[0])
			if e != nil || !json.Valid(raw) {
				t.Fatal("history archive invalid")
			}
		})
	}
}
