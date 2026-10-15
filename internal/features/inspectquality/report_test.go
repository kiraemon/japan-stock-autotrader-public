package inspectquality

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func example(start time.Time) snapshotInput {
	received := start.Add(400 * time.Millisecond)
	board, _ := json.Marshal(map[string]any{"CurrentPrice": 1234, "CurrentPriceTime": start.Add(-5 * time.Second), "private_marker": "must-never-be-in-report"})
	return snapshotInput{Version: 1, Environment: "production", StartedAt: start, FinishedAt: start.Add(time.Second), Complete: true, Records: []recordInput{
		{Endpoint: "/wallet/cash", ReceivedAt: received, Data: json.RawMessage(`{"StockAccountWallet":987654321,"private_marker":"must-never-be-in-report"}`)},
		{Endpoint: "/wallet/margin", ReceivedAt: received, Data: json.RawMessage(`{}`)},
		{Endpoint: "/positions?product=1&addinfo=true", ReceivedAt: received, Data: json.RawMessage(`[]`)},
		{Endpoint: "/positions?product=2&addinfo=true", ReceivedAt: received, Data: json.RawMessage(`[]`)},
		{Endpoint: "/board/7203@1", ReceivedAt: received, Data: board},
	}}
}

func writeExample(t *testing.T, dir, name string, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestChronologicalInspectionAndRepeatedPricesAreInformational(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 15, 6, 30, 0, 0, time.UTC)
	first := example(start)
	second := example(start.Add(11 * time.Second))
	second.Records[4].Data = first.Records[4].Data // unchanged market time is not a communication failure
	before := writeExample(t, dir, "snapshot-z.json", first)
	writeExample(t, dir, "snapshot-a.json", second)
	if err := os.WriteFile(filepath.Join(dir, ".snapshot-in-progress.tmp"), []byte("in progress"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Inspect(context.Background(), dir, "production")
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 2 || r.ValidFiles != 2 || r.Records != 10 || r.BoardRecords != 2 || r.NeedsReview {
		t.Fatalf("unexpected report: %+v", r)
	}
	if r.PostCollectionGap.Count != 1 || r.PostCollectionGap.Min != 10 || r.PostCollectionGap.Max != 10 {
		t.Fatal("filename order used instead of collection time")
	}
	if r.RepeatedMarketTimes != 1 || r.PriceAgeAtReceive.Count != 2 || r.PriceAgeAtReceive.Min != 5.4 || r.PriceAgeAtReceive.Max != 16.4 {
		t.Fatal("market time/receive time not separated")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "snapshot-z.json"))
	if string(before) != string(after) {
		t.Fatal("source snapshot changed")
	}
	path, err := Save(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"must-never-be-in-report", "987654321", "StockAccountWallet", "7203", `"endpoint":`, "private_marker"} {
		if strings.Contains(string(output), forbidden) {
			t.Fatalf("private input exposed in report: %s", forbidden)
		}
	}
}

func TestCorruptUnsupportedMismatchedAndPartialFiles(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(dir, "snapshot-corrupt.json"), []byte(`{"password":"must-never-be-in-report"`), 0600); err != nil {
		t.Fatal(err)
	}
	unsupported := example(start)
	unsupported.Version = 2
	writeExample(t, dir, "snapshot-unsupported.json", unsupported)
	mismatch := example(start)
	mismatch.Environment = "test"
	writeExample(t, dir, "snapshot-wrong-env.json", mismatch)
	partial := example(start)
	partial.Complete = false
	partial.Records = partial.Records[:2]
	partial.Records[1].Error = "must-never-be-in-report"
	writeExample(t, dir, "snapshot-partial.json", partial)
	r, err := Inspect(context.Background(), dir, "production")
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 4 || r.ValidFiles != 1 || r.InvalidFiles != 3 || r.UnsupportedSchemaFiles != 1 || r.EnvironmentMismatchFiles != 1 || r.IncompleteFiles != 1 || r.APIErrorRecords != 1 || r.FilesMissingRequiredAPIs != 1 || !r.NeedsReview {
		t.Fatalf("unexpected findings: %+v", r)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "must-never-be-in-report") {
		t.Fatal("input error text leaked")
	}
}

func TestMissingAndInvalidFields(t *testing.T) {
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func(*snapshotInput)
		check  func(Report) bool
	}{
		{"nulls", func(s *snapshotInput) {
			s.Records[4].Data = json.RawMessage(`{"CurrentPrice":null,"CurrentPriceTime":null}`)
		}, func(r Report) bool { return r.MissingPrices == 1 && r.MissingMarketTimes == 1 }},
		{"invalid_board", func(s *snapshotInput) { s.Records[4].Data = json.RawMessage(`{"CurrentPrice":"wrong"}`) }, func(r Report) bool { return r.InvalidDataRecords == 1 }},
		{"invalid_positions", func(s *snapshotInput) { s.Records[2].Data = json.RawMessage(`{}`) }, func(r Report) bool { return r.InvalidDataRecords == 1 }},
		{"negative_price", func(s *snapshotInput) { s.Records[4].Data = json.RawMessage(`{"CurrentPrice":-1}`) }, func(r Report) bool { return r.NonPositivePrices == 1 }},
		{"future_market", func(s *snapshotInput) {
			raw, _ := json.Marshal(map[string]any{"CurrentPrice": 1, "CurrentPriceTime": start.Add(time.Hour)})
			s.Records[4].Data = raw
		}, func(r Report) bool { return r.MarketTimeAheadOfReceive == 1 }},
		{"receive_reverse", func(s *snapshotInput) { s.Records[1].ReceivedAt = start.Add(-time.Second) }, func(r Report) bool { return r.InvalidReceiveTimes > 0 }},
		{"missing_api", func(s *snapshotInput) { s.Records = s.Records[:4] }, func(r Report) bool { return r.FilesMissingRequiredAPIs == 1 && r.InconsistentCompleteFiles == 1 }},
		{"duplicate", func(s *snapshotInput) { s.Records = append(s.Records, s.Records[4]) }, func(r Report) bool { return r.DuplicateEndpointRecords == 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := example(start)
			tc.mutate(&s)
			writeExample(t, dir, "snapshot-1.json", s)
			r, err := Inspect(context.Background(), dir, "production")
			if err != nil || !r.NeedsReview || !tc.check(r) {
				t.Fatalf("missing finding: %+v err=%v", r, err)
			}
		})
	}
}

func TestBackwardMarketAndOverlappingRuns(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	first := example(start)
	second := example(start.Add(500 * time.Millisecond))
	raw, _ := json.Marshal(map[string]any{"CurrentPrice": 1, "CurrentPriceTime": start.Add(-time.Hour)})
	second.Records[4].Data = raw
	writeExample(t, dir, "snapshot-1.json", first)
	writeExample(t, dir, "snapshot-2.json", second)
	r, err := Inspect(context.Background(), dir, "production")
	if err != nil || r.OverlappingSnapshots != 1 || r.BackwardMarketTimes != 1 || !r.NeedsReview {
		t.Fatalf("missing temporal findings: %+v %v", r, err)
	}
}

func TestCancellationEmptyDirectoryAndOversize(t *testing.T) {
	dir := t.TempDir()
	if _, err := Inspect(context.Background(), dir, "production"); err == nil {
		t.Fatal("empty input accepted")
	}
	path := filepath.Join(dir, "snapshot-too-large.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((64 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err := Inspect(context.Background(), dir, "production")
	if err != nil || r.InvalidFiles != 1 || !r.NeedsReview {
		t.Fatal("oversized input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, dir, "production"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
