package capturehistory

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

type fakeReader struct {
	calls  int
	failAt int
}

func (f *fakeReader) Board(context.Context, string) (json.RawMessage, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, errors.New("synthetic failure")
	}
	return json.RawMessage(`{"Symbol":"7203","Exchange":1,"TradingVolume":30}`), nil
}
func (f *fakeReader) TimeAndSales(context.Context, string) (json.RawMessage, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, errors.New("synthetic failure")
	}
	return json.RawMessage(`{"Symbol":"7203","Exchange":1,"TradingPriceCount":2,"TradingPrice":[{"Time":"2026-10-15T09:00:00+09:00","Price":100.5,"Volume":10},{"Time":"2026-10-15T09:00:00+09:00","Price":101,"Volume":20}]}`), nil
}

func TestCapturePreservesOriginalRowsAndIndependentArchives(t *testing.T) {
	dir := t.TempDir()
	f := &fakeReader{}
	var out bytes.Buffer
	for range 2 {
		if err := Capture(context.Background(), f, "test", []string{"7203@1"}, dir, &out); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "history-*.json"))
	if len(files) != 2 || f.calls != 6 {
		t.Fatal("archive overwritten or unexpected calls")
	}
	for _, path := range files {
		raw, _ := os.ReadFile(path)
		var a Archive
		if json.Unmarshal(raw, &a) != nil || !a.Complete || a.Version != 1 || a.HistoryReceivedAt == nil || a.Analysis.Rows != 2 || !a.Analysis.CountMatches || a.Analysis.FullHistoryVerified || a.Analysis.VolumeSemantics != "unverified" {
			t.Fatal("archive contract invalid")
		}
		var data struct{ TradingPrice []json.RawMessage }
		if json.Unmarshal(a.Trades, &data) != nil || len(data.TradingPrice) != 2 {
			t.Fatal("same-second rows lost")
		}
	}
}

func TestCaptureFailurePreservesPartialAndStops(t *testing.T) {
	for _, fail := range []int{1, 2, 3} {
		t.Run(string(rune('0'+fail)), func(t *testing.T) {
			dir := t.TempDir()
			f := &fakeReader{failAt: fail}
			err := Capture(context.Background(), f, "test", []string{"7203@1", "8306@1"}, dir, &bytes.Buffer{})
			files, _ := filepath.Glob(filepath.Join(dir, "history-*.json"))
			if err == nil || f.calls != fail || len(files) != 1 {
				t.Fatal("failure did not stop and preserve")
			}
			raw, _ := os.ReadFile(files[0])
			var a Archive
			if json.Unmarshal(raw, &a) != nil || a.Complete || a.Error == "" {
				t.Fatal("partial capture lost")
			}
		})
	}
}

func TestAnalysisVolumeEvidenceAndCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, volumes string
		sum, max      bool
	}{{"per trade", "10,20", true, false}, {"cumulative", "10,30", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			a := Archive{Symbol: "7203@1", Complete: true, StartedAt: time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC), BoardBefore: json.RawMessage(`{"Symbol":"7203","Exchange":1,"TradingVolume":29}`), BoardAfter: json.RawMessage(`{"Symbol":"7203","Exchange":1,"TradingVolume":31}`)}
			var v []json.Number
			_ = json.Unmarshal([]byte("["+tc.volumes+"]"), &v)
			a.Trades, _ = json.Marshal(map[string]any{"Symbol": "7203", "Exchange": "1", "TradingPriceCount": 3, "TradingPrice": []any{map[string]any{"Time": "2026-10-16T09:01:00+09:00", "Price": 100.5, "Volume": v[1]}, map[string]any{"Time": "2026-10-15T15:30:00+09:00", "Price": 100, "Volume": 500}, map[string]any{"Time": "2026-10-16T09:00:00+09:00", "Price": 100, "Volume": v[0]}}})
			r := Analyze(a)
			if !r.Parsed || !r.IdentityMatches || !r.CountMatches || r.InvalidRows != 0 || len(r.Days) != 2 || r.Days[1].Rows != 2 || r.Days[1].First.Hour() != 9 || r.SumVolumeMatchesBracket != tc.sum || r.MaxVolumeMatchesBracket != tc.max || r.FullHistoryVerified || r.VolumeSemantics != "unverified" {
				t.Fatalf("analysis=%+v", r)
			}
		})
	}
}

func TestAnalysisRejectsInvalidAndMissingValues(t *testing.T) {
	for _, raw := range []string{`{}`, `{"TradingPrice":null}`, `{"TradingPrice":[]}`, `{"Symbol":"8306","Exchange":1,"TradingPriceCount":0,"TradingPrice":[]}`, `{"Symbol":"7203","Exchange":1,"TradingPriceCount":2,"TradingPrice":[{"Time":"broken","Price":100,"Volume":10}]}`, `{"Symbol":"7203","Exchange":1,"TradingPriceCount":1,"TradingPrice":[{"Time":"2026-10-16T09:00:00+09:00","Price":null,"Volume":10}]}`} {
		r := Analyze(Archive{Symbol: "7203@1", Trades: json.RawMessage(raw), FinishedAt: time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)})
		if r.Parsed && r.IdentityMatches && r.CountMatches && r.InvalidRows == 0 {
			t.Fatalf("invalid record accepted: %s", raw)
		}
	}
}

func TestCanceledAndInvalidRequestsMakeNoCalls(t *testing.T) {
	f := &fakeReader{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Capture(ctx, f, "test", []string{"7203@1"}, t.TempDir(), &bytes.Buffer{}) == nil || f.calls != 0 {
		t.Fatal("canceled capture read broker")
	}
	if Capture(context.Background(), f, "test", []string{"7203@1/../token"}, t.TempDir(), &bytes.Buffer{}) == nil || f.calls != 0 {
		t.Fatal("invalid symbol read broker")
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if Capture(context.Background(), &fakeReader{}, "test", []string{"7203@1"}, path, &bytes.Buffer{}) == nil {
		t.Fatal("save failure ignored")
	}
}
