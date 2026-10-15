package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/instanceguard"
	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
	"github.com/kiraemon/japan-stock-autotrader/internal/telemetry"
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
	closed   bool
}

func (s *observedSession) Authenticate(context.Context, string) error {
	if s.failAuth {
		return errors.New("synthetic auth failure")
	}
	return nil
}
func (s *observedSession) Close() { s.closed = true }
func (s *observedSession) Cash(context.Context) (json.RawMessage, error) {
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
