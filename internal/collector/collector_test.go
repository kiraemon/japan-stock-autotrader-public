package collector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"japan-stock-autotrader/internal/kabu"
)

func TestSavePreservesPartialRun(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	run := Snapshot{SchemaVersion: 1, Environment: kabu.Test, StartedAt: time.Now().UTC(), Complete: false, Records: []Record{
		{Endpoint: "/positions?product=2&addinfo=true", ReceivedAt: time.Now().UTC(), Data: json.RawMessage(`[]`)},
		{Endpoint: "/board/7203@1", ReceivedAt: time.Now().UTC(), Error: "API HTTP 429"},
	}}
	first, err := Save(directory, run)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Save(directory, run)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("overwrote earlier run")
	}
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Complete || len(got.Records) != 2 || string(got.Records[0].Data) != "[]" || got.Records[1].Error == "" {
		t.Fatal("partial run corrupted")
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatal("temporary files left behind")
	}
}

type fakeReader struct {
	calls  int
	failAt int
}

func (f *fakeReader) read() (json.RawMessage, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, errors.New("simulated HTTP 429")
	}
	return json.RawMessage(`{"CurrentPrice":null}`), nil
}
func (f *fakeReader) Cash(context.Context) (json.RawMessage, error)   { return f.read() }
func (f *fakeReader) Margin(context.Context) (json.RawMessage, error) { return f.read() }
func (f *fakeReader) Positions(context.Context, bool) (json.RawMessage, error) {
	_, err := f.read()
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`[]`), nil
}
func (f *fakeReader) Board(context.Context, string) (json.RawMessage, error) { return f.read() }

func TestCollectStopsOnFailureAndMarksCompleteOnlyAfterAllReads(t *testing.T) {
	for _, failAt := range []int{0, 1, 3, 5} {
		reader := &fakeReader{failAt: failAt}
		run, err := Collect(context.Background(), reader, kabu.Test, []string{"7203@1"})
		if failAt == 0 {
			if err != nil || !run.Complete || len(run.Records) != 5 {
				t.Fatal("complete run not recorded")
			}
			if string(run.Records[2].Data) != "[]" {
				t.Fatal("empty positions lost")
			}
		} else {
			if err == nil || run.Complete || len(run.Records) != failAt || reader.calls != failAt {
				t.Fatal("did not stop at failure")
			}
			if run.Records[failAt-1].Error == "" {
				t.Fatal("missing error record")
			}
		}
		if run.StartedAt.IsZero() || run.FinishedAt.IsZero() {
			t.Fatal("missing timestamps")
		}
	}
}
