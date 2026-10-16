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

func TestBeforeCollectFailureStopsBeforeSnapshotAndSharesDeadline(t *testing.T) {
	directory := t.TempDir()
	reader := &fakeReader{}
	failure := errors.New("synthetic history save failure")
	calls := 0
	err := Run(context.Background(), reader, Options{Environment: "test", Symbols: []string{"7203@1"}, Count: 2, Interval: time.Second, Directory: directory, Until: time.Now().Add(time.Hour), BeforeCollect: func(ctx context.Context) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("capture not inside run deadline")
		}
		return failure
	}}, &bytes.Buffer{})
	files, _ := filepath.Glob(filepath.Join(directory, "snapshot-*.json"))
	if !errors.Is(err, failure) || calls != 1 || reader.calls != 0 || len(files) != 0 {
		t.Fatal("capture failure did not stop collection")
	}
}

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

func TestObserverFailureStopsBeforeBrokerRead(t *testing.T) {
	reader := &fakeReader{}
	err := Run(context.Background(), reader, Options{Environment: "test", Symbols: []string{"7203@1"}, Count: 1, Interval: time.Second, Directory: t.TempDir(), Observe: func(Progress) error { return errors.New("status unavailable") }}, &bytes.Buffer{})
	if err == nil || reader.calls != 0 {
		t.Fatal("broker used after status failure")
	}
}

func TestObserverRecordsPartialSavedBeforeFailure(t *testing.T) {
	reader := &fakeReader{failAt: 3}
	var events []Progress
	err := Run(context.Background(), reader, Options{Environment: "test", Symbols: []string{"7203@1"}, Count: 2, Interval: time.Second, Directory: t.TempDir(), Observe: func(p Progress) error { events = append(events, p); return nil }}, &bytes.Buffer{})
	if err == nil || len(events) != 2 || events[0].Phase != "collecting" || events[1].Phase != "saved" || events[1].Complete {
		t.Fatal("partial observation incorrect")
	}
}

type deadlineReader struct {
	fakeReader
}

func (r *deadlineReader) Cash(ctx context.Context) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestScheduledEndDuringWaitAndRead(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "reading"}[duringRead], func(t *testing.T) {
			directory := t.TempDir()
			var client Reader = &fakeReader{}
			if duringRead {
				client = &deadlineReader{}
			}
			var output bytes.Buffer
			deadline := time.Now().Add(time.Second)
			err := Run(context.Background(), client, Options{Environment: "test", Symbols: []string{"7203@1"}, Count: 10000, Interval: time.Hour, Directory: directory, Until: deadline}, &output)
			if !errors.Is(err, ErrScheduledEnd) || time.Now().Before(deadline) || !bytes.Contains(output.Bytes(), []byte("終了日時に到達")) {
				t.Fatalf("scheduled end failed: %v", err)
			}
			files, _ := filepath.Glob(filepath.Join(directory, "snapshot-*.json"))
			if len(files) != 1 {
				t.Fatalf("unexpected snapshots: %d", len(files))
			}
			raw, _ := os.ReadFile(files[0])
			var snapshot Snapshot
			if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.Complete == duringRead {
				t.Fatalf("partial record lost: %v", err)
			}
			if duringRead && (len(snapshot.Records) != 1 || snapshot.Records[0].Error == "") {
				t.Fatal("in-flight deadline not saved")
			}
			t.Log(output.String())
		})
	}
}

func TestScheduledEndDoesNotHideFailures(t *testing.T) {
	for _, tc := range []string{"api timeout", "save failure", "parent cancellation"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			directory := t.TempDir()
			client := &fakeReader{}
			options := Options{Environment: "test", Symbols: []string{"7203@1"}, Count: 2, Interval: time.Hour, Directory: directory, Until: time.Now().Add(time.Second)}
			var reader Reader = client
			switch tc {
			case "api timeout":
				reader = &timeoutReader{}
			case "save failure":
				options.Directory = filepath.Join(directory, "file")
				if err := os.WriteFile(options.Directory, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
			case "parent cancellation":
				options.Observe = func(p Progress) error {
					if p.Phase == "waiting" {
						cancel()
					}
					return nil
				}
			}
			var output bytes.Buffer
			if err := Run(ctx, reader, options, &output); err == nil || bytes.Contains(output.Bytes(), []byte("終了日時に到達")) {
				t.Fatal("failure was converted to scheduled success")
			}
		})
	}
}

type timeoutReader struct{ fakeReader }

func (timeoutReader) Cash(context.Context) (json.RawMessage, error) {
	return nil, context.DeadlineExceeded
}
