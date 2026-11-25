package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/kabu"
	"github.com/kiraemon/japan-stock-trading-lab/internal/telemetry"
)

// TestTerminalHelper is opt-in: run the compiled test binary in a real terminal
// with JSA_TERMINAL_CASE=reading or waiting, then send Ctrl+C. All API methods
// below are synthetic. Only the production startup gate is used.
func TestTerminalHelper(t *testing.T) {
	mode := os.Getenv("JSA_TERMINAL_CASE")
	if mode == "" {
		return
	}
	if mode != "reading" && mode != "waiting" {
		t.Fatal("unknown terminal case")
	}
	if root := os.Getenv("JSA_TERMINAL_ROOT"); root != "" {
		t.Chdir(root)
	} else {
		t.Chdir(t.TempDir())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := runWithSession(ctx,
		[]string{"-symbols", "7203@1", "-password-stdin", "-count", "2", "-interval", "1h"},
		strings.NewReader("test-only-password\n"), os.Stdout,
		func(env kabu.Environment) (session, error) {
			owner, err := kabu.Open(env)
			if err != nil {
				return nil, err
			}
			return &terminalSession{owner: owner, reading: mode == "reading"}, nil
		})
	if ctx.Err() == nil || err == nil {
		t.Fatalf("expected interrupt cancellation, got %v", err)
	}
	observed := telemetry.Observe("data", "test", time.Now().UTC())
	if observed.State != "interrupted" || observed.Saved != 1 {
		t.Fatalf("interrupted status not saved: %s / %d", observed.State, observed.Saved)
	}
	fmt.Println("TEST_STATUS=interrupted / saved=1")
	fmt.Println("TEST_RESULT=interrupted-and-session-closed")
}

type terminalSession struct {
	owner   *kabu.Client
	reading bool
}

func (s *terminalSession) Authenticate(context.Context, string) error { return nil }
func (s *terminalSession) Close()                                     { s.owner.Close() }
func (s *terminalSession) Cash(ctx context.Context) (json.RawMessage, error) {
	if s.reading {
		fmt.Println("TEST_STAGE=reading")
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return json.RawMessage(`{"StockAccountWallet":0}`), nil
}
func (s *terminalSession) Margin(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"MarginAccountWallet":0}`), nil
}
func (s *terminalSession) Positions(context.Context, bool) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (s *terminalSession) Board(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"CurrentPrice":null}`), nil
}
