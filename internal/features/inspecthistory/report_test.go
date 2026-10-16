package inspecthistory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture() string {
	return `{"schema_version":1,"environment":"production","symbol":"7203@1","complete":true,"started_at":"2026-10-16T10:01:00+09:00","history_received_at":"2026-10-16T10:01:30+09:00","finished_at":"2026-10-16T10:01:31+09:00","board_after":{"Symbol":"7203","Exchange":1,"OpeningPrice":101,"HighPrice":102,"LowPrice":101,"CurrentPrice":102,"TradingVolume":40,"OpeningPriceTime":"2026-10-16T09:03:00+09:00","CurrentPriceTime":"2026-10-16T10:01:01+09:00","TradingVolumeTime":"2026-10-16T10:01:01+09:00"},"time_and_sales":{"Symbol":"7203","Exchange":"1","TradingPriceCount":4,"TradingPrice":[{"Time":"2026-10-15T09:00:00+09:00","Price":100.1,"Volume":0.1},{"Time":"2026-10-15T15:30:00+09:00","Price":100.2,"Volume":0.2},{"Time":"2026-10-16T09:03:00+09:00","Price":101,"Volume":15},{"Time":"2026-10-16T10:01:01+09:00","Price":102,"Volume":25}]}}`
}

func TestCoverageComparisonsAndUnknownReference(t *testing.T) {
	c := analyze([]byte(fixture()), "history-synthetic.json", "production", nil)
	if !c.Valid || c.Rows != 4 || len(c.Days) != 2 || c.BackwardSteps != 0 || c.Days[0].Matches != nil {
		t.Fatalf("coverage=%+v", c)
	}
	d := c.Days[1]
	m := d.Matches
	if d.MinutesWithTrades != 2 || d.UnfinishedMinutes != 1 || m == nil || !m.Open || !m.High || !m.Low || !m.Last || !m.Volume || !m.OpeningTime || !m.LastTime {
		t.Fatalf("today=%+v matches=%+v", d, m)
	}
	if c.Days[0].Last.Hour() != 15 || c.Days[0].Last.Minute() != 30 {
		t.Fatal("closing auction row lost")
	}
}

func priorReference(t *testing.T) reference {
	t.Helper()
	var b board
	if json.Unmarshal([]byte(`{"Symbol":"7203","Exchange":1,"OpeningPrice":100.1,"HighPrice":100.2,"LowPrice":100.1,"CurrentPrice":100.2,"TradingVolume":0.3,"OpeningPriceTime":"2026-10-15T09:00:00+09:00","CurrentPriceTime":"2026-10-15T15:30:00+09:00","TradingVolumeTime":"2026-10-15T15:30:00+09:00"}`), &b) != nil {
		t.Fatal("invalid fixture")
	}
	return reference{board: b, file: "snapshot-synthetic.json", received: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)}
}

func TestExactDecimalVolumeAndFutureReferenceExcluded(t *testing.T) {
	ref := priorReference(t)
	c := analyze([]byte(fixture()), "history-synthetic.json", "production", map[string][]reference{"7203@1/2026-10-15": {ref}})
	if c.Days[0].Matches == nil || !c.Days[0].Matches.Volume {
		t.Fatal("0.1+0.2 must exactly equal 0.3")
	}
	ref.received = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	c = analyze([]byte(fixture()), "history-synthetic.json", "production", map[string][]reference{"7203@1/2026-10-15": {ref}})
	if c.Days[0].Matches != nil {
		t.Fatal("reference from after archive capture used")
	}
}

func TestWrongOrIncompleteInputsAndBoundedNumbers(t *testing.T) {
	for _, tc := range []struct{ old, new string }{
		{`"complete":true`, `"complete":false`},
		{`"environment":"production"`, `"environment":"test"`},
		{`"TradingPriceCount":4`, `"TradingPriceCount":5`},
		{`"Exchange":"1"`, `"Exchange":"3"`},
		{`"Volume":0.1`, `"Volume":null`},
		{`"Price":100.1`, `"Price":1e999999999`},
		{`2026-10-15T09:00:00+09:00`, `2026-10-17T09:00:00+09:00`},
	} {
		if analyze([]byte(strings.Replace(fixture(), tc.old, tc.new, 1)), "history.json", "production", nil).Valid {
			t.Fatalf("invalid input accepted: %s", tc.new)
		}
	}
	for _, n := range []json.Number{"1e999999999", "1e-999999999", ""} {
		if _, ok := rat(n); ok {
			t.Fatal("unsafe number accepted")
		}
	}
	// A quote with the wrong volume date is unavailable, not a false zero match.
	raw := strings.Replace(fixture(), `"TradingVolumeTime":"2026-10-16T10:01:01+09:00"`, `"TradingVolumeTime":"2026-10-15T10:01:01+09:00"`, 1)
	if analyze([]byte(raw), "history.json", "production", nil).Days[1].Matches != nil {
		t.Fatal("stale quote accepted")
	}
}

func TestEqualTimeRowsArePreservedAndBackwardsAreReported(t *testing.T) {
	raw := strings.Replace(fixture(), `2026-10-16T10:01:01+09:00","Price":102`, `2026-10-16T09:03:00+09:00","Price":102`, 1)
	c := analyze([]byte(raw), "history.json", "production", nil)
	if !c.Valid || c.EqualTimeSteps != 1 || c.Days[1].Rows != 2 {
		t.Fatal("same-time rows collapsed")
	}
	if c.Days[1].OpenOrderSensitiveMinutes != 1 || c.Days[1].CloseOrderSensitiveMinutes != 1 {
		t.Fatal("same-time distinct prices must expose order-sensitive minute boundaries")
	}
	raw = strings.Replace(fixture(), `2026-10-16T10:01:01+09:00","Price":102`, `2026-10-16T09:02:00+09:00","Price":102`, 1)
	if analyze([]byte(raw), "history.json", "production", nil).BackwardSteps != 1 {
		t.Fatal("backwards time hidden")
	}
}

func TestOfflineInspectionAndAtomicReportsDoNotExposeAccountData(t *testing.T) {
	root := t.TempDir()
	histories := filepath.Join(root, "history")
	snapshots := filepath.Join(root, "snapshots")
	for _, dir := range []string{histories, snapshots} {
		if os.MkdirAll(dir, 0700) != nil {
			t.Fatal("mkdir")
		}
	}
	if os.WriteFile(filepath.Join(histories, "history-test.json"), []byte(fixture()), 0600) != nil {
		t.Fatal("write history")
	}
	ref := priorReference(t)
	b, _ := json.Marshal(ref.board)
	s := map[string]any{"schema_version": 1, "environment": "production", "complete": true, "started_at": "2026-10-15T16:00:00+09:00", "finished_at": "2026-10-15T16:00:03+09:00", "records": []any{map[string]any{"endpoint": "/wallet/cash", "received_at": "2026-10-15T16:00:00+09:00", "data": map[string]any{"secret": "synthetic-account-marker"}}, map[string]any{"endpoint": "/board/7203@1", "received_at": "2026-10-15T16:00:01+09:00", "data": json.RawMessage(b)}}}
	raw, _ := json.Marshal(s)
	if os.WriteFile(filepath.Join(snapshots, "snapshot-test.json"), raw, 0600) != nil {
		t.Fatal("write snapshot")
	}
	r, err := Inspect(context.Background(), histories, snapshots, "production")
	if err != nil || len(r.Archives) != 1 || r.Archives[0].Days[0].Matches == nil || !r.Archives[0].Days[0].Matches.Volume || r.FullHistoryVerified || r.IndependentSourceVerified {
		t.Fatalf("offline audit=%+v err=%v", r, err)
	}
	path, err := Save(filepath.Join(root, "quality"), r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Save(filepath.Join(root, "quality"), r)
	if err != nil || path == second {
		t.Fatal("report overwritten")
	}
	out, _ := os.ReadFile(path)
	for _, forbidden := range []string{"synthetic-account-marker", "OpeningPrice", "TradingVolume", "100.1"} {
		if strings.Contains(string(out), forbidden) {
			t.Fatal("raw data leaked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, histories, snapshots, "production"); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := Inspect(context.Background(), filepath.Join(root, "absent"), snapshots, "production"); err == nil {
		t.Fatal("missing history accepted")
	}
}
