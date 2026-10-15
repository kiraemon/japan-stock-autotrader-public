package collectquotes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunSixSnapshotsAndPreservesSchema(t *testing.T) {
	directory := t.TempDir()
	reader := &fakeReader{}
	var output bytes.Buffer
	err := Run(context.Background(), reader, Options{
		Environment: "test", Symbols: []string{"7203@1"}, Count: 6,
		Interval: time.Second, Directory: directory,
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "snapshot-*.json"))
	if err != nil || len(files) != 6 || reader.calls != 30 {
		t.Fatalf("files=%d calls=%d err=%v", len(files), reader.calls, err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var saved Snapshot
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.SchemaVersion != 1 || !saved.Complete || len(saved.Records) != 5 {
			t.Fatal("snapshot schema/contents changed")
		}
	}
}

func TestRunStopsAndPreservesPartialFailure(t *testing.T) {
	directory := t.TempDir()
	reader := &fakeReader{failAt: 3}
	err := Run(context.Background(), reader, Options{
		Environment: "test", Symbols: []string{"7203@1"}, Count: 6,
		Interval: time.Second, Directory: directory,
	}, &bytes.Buffer{})
	if err == nil || reader.calls != 3 {
		t.Fatal("collection failure did not stop the run")
	}
	files, _ := filepath.Glob(filepath.Join(directory, "snapshot-*.json"))
	if len(files) != 1 {
		t.Fatalf("files=%d", len(files))
	}
	raw, _ := os.ReadFile(files[0])
	var saved Snapshot
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Complete || len(saved.Records) != 3 || saved.Records[2].Error == "" {
		t.Fatal("partial failure was lost")
	}
}

func TestRunStopsOnSaveFailure(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("test-only"), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &fakeReader{}
	err := Run(context.Background(), reader, Options{
		Environment: "test", Symbols: []string{"7203@1"}, Count: 6,
		Interval: time.Second, Directory: blocked,
	}, &bytes.Buffer{})
	if err == nil || reader.calls != 5 {
		t.Fatal("save failure did not stop the run")
	}
}

func TestRepeatCountAndSpacing(t *testing.T) {
	const interval = 5 * time.Millisecond
	var finished time.Time
	calls := 0
	err := repeat(context.Background(), 3, interval, func() error {
		if !finished.IsZero() && time.Since(finished) < interval {
			t.Fatal("next collection started before the post-save interval")
		}
		calls++
		finished = time.Now()
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRepeatStopsOnFailure(t *testing.T) {
	failure := errors.New("collection or save failed")
	calls := 0
	err := repeat(context.Background(), 10, time.Millisecond, func() error {
		calls++
		return failure
	})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRepeatCancellationDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	calls := 0
	go func() {
		done <- repeat(ctx, 3, time.Hour, func() error {
			calls++
			close(started)
			return nil
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the wait")
	}
}

func TestRepeatAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := repeat(ctx, 1, time.Second, func() error {
		t.Fatal("collection started after cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
