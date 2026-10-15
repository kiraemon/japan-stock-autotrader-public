package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatPhasesTerminalHistoryAndEnvironment(t *testing.T) {
	root := t.TempDir()
	s, err := start(context.Background(), root, "test", 2, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Finish("interrupted") })
	before := Observe(root, "test", time.Now().UTC())
	if before.State != "active" || before.Phase != "starting" {
		t.Fatal("missing starting observation")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		observed := Observe(root, "test", time.Now().UTC())
		if observed.UpdatedAt != nil && observed.UpdatedAt.After(*before.UpdatedAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat did not advance")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Phase("waiting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Saved(true); err != nil {
		t.Fatal(err)
	}
	if err := s.Saved(true); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("completed"); err != nil {
		t.Fatal(err)
	}
	after := Observe(root, "test", time.Now().UTC().Add(time.Hour))
	if after.State != "completed" || after.Saved != 2 || after.Planned != 2 {
		t.Fatal("terminal state incorrectly expired")
	}
	if Observe(root, "production", time.Now()).State != "unrecorded" {
		t.Fatal("environment fallback")
	}
	files, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "run-*.json"))
	if len(files) != 1 {
		t.Fatal("daily summary missing")
	}
	raw, _ := os.ReadFile(files[0])
	var final Record
	if json.Unmarshal(raw, &final) != nil || final.RunID == "" || final.Complete != 2 || final.Partial != 0 {
		t.Fatal("incorrect summary")
	}
	if err := s.Phase("collecting"); err == nil {
		t.Fatal("closed session accepted progress")
	}
}

func TestObservationStalenessClockAndCorruption(t *testing.T) {
	root := t.TempDir()
	s, err := start(context.Background(), root, "production", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finish("interrupted")
	if Observe(root, "production", time.Now().Add(21*time.Second)).State != "stale" {
		t.Fatal("dead heartbeat shown active")
	}
	if Observe(root, "production", time.Now().Add(-time.Minute)).State != "clock_error" {
		t.Fatal("future heartbeat shown active")
	}
	if err := s.Saved(false); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("failed"); err != nil {
		t.Fatal(err)
	}
	if Observe(root, "production", time.Now()).State != "failed" {
		t.Fatal("partial failure missing")
	}
	path := filepath.Join(root, "runtime", "production.json")
	for _, raw := range []string{`{"password":"never-echo-this"}`, strings.Repeat("x", 4097), `{}`, `null`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		view := Observe(root, "production", time.Now())
		b, _ := json.Marshal(view)
		if view.State != "unavailable" || strings.Contains(string(b), "never-echo-this") {
			t.Fatal("invalid status accepted or exposed")
		}
	}
}

func TestWriteFailureCancelsWork(t *testing.T) {
	root := t.TempDir()
	s, err := start(context.Background(), root, "test", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runtime", "test.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Phase("collecting"); err == nil {
		t.Fatal("write failure ignored")
	}
	select {
	case <-s.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("work not canceled")
	}
	if s.Err() == nil {
		t.Fatal("failure lost")
	}
	if err := s.Finish("failed"); err == nil {
		t.Fatal("failed final write ignored")
	}
}

func TestConcurrentReadAndReplace(t *testing.T) {
	root := t.TempDir()
	s, err := start(context.Background(), root, "test", 1, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finish("interrupted")
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				Observe(root, "test", time.Now())
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for i := 0; i < 100; i++ {
		if err := s.Phase("collecting"); err != nil {
			var link *os.LinkError
			if errors.As(err, &link) {
				t.Fatalf("reader prevented replacement: OS code=%v", link.Err)
			}
			t.Fatalf("reader prevented replacement: %v", err)
		}
	}
	if s.Err() != nil {
		t.Fatal("heartbeat failed during normal reads")
	}
}

func TestForcedExitLeavesUnconfirmedStatusAndRestartReplacesIt(t *testing.T) {
	if root := os.Getenv("JSA_TELEMETRY_CHILD_ROOT"); root != "" {
		if _, err := start(context.Background(), root, "test", 1, 10*time.Millisecond); err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("READY\n")
		select {}
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestForcedExitLeavesUnconfirmedStatusAndRestartReplacesIt$")
	cmd.Env = append(os.Environ(), "JSA_TELEMETRY_CHILD_ROOT="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	var previous Observation
	for {
		previous = Observe(root, "test", time.Now())
		if previous.State == "active" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if Observe(root, "test", previous.UpdatedAt.Add(21*time.Second)).State != "stale" {
		t.Fatal("killed process not unconfirmed after lease")
	}
	s, err := start(context.Background(), root, "test", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("interrupted"); err != nil {
		t.Fatal(err)
	}
	if Observe(root, "test", time.Now()).State != "interrupted" {
		t.Fatal("restart did not replace stale state")
	}
}
