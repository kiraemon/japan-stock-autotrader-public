package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

func TestDiagnosticRawInputsReplayAndBothSourceHashes(t *testing.T) {
	t.Chdir(t.TempDir())
	var csv strings.Builder
	csv.WriteString("日付,始値,高値,安値,終値,売買高\r\n")
	rows := []map[string]any{}
	d := backtest.Synthetic(r001.Long)
	for _, b := range d.Days[0].Bars {
		fmt.Fprintf(&csv, "%s,%d,%d,%d,%d,400\r\n", b.Start.Format("2006/01/02 15:04:05"), b.Open/10000, b.High/10000, b.Low/10000, b.Close/10000)
		for i, p := range []int64{b.Open, b.High, b.Low, b.Close} {
			rows = append(rows, map[string]any{"Time": b.Start.Add(time.Duration(i*15) * time.Second), "Price": p / 10000, "Volume": 100})
		}
	}
	at := d.Source.ReceivedAt
	history, _ := json.Marshal(map[string]any{"schema_version": 1, "environment": "test", "symbol": "0000@1", "complete": true, "started_at": at, "history_received_at": at, "finished_at": at, "time_and_sales": map[string]any{"Symbol": "0000", "Exchange": 1, "TradingPriceCount": len(rows), "TradingPrice": rows}})
	if os.WriteFile("chart.csv", []byte(csv.String()), 0600) != nil || os.WriteFile("ticks.json", history, 0600) != nil {
		t.Fatal("fixture write")
	}
	args := []string{"diagnose", "-input", "chart.csv", "-reference", "ticks.json", "-symbol", "0000@1", "-environment", "test", "-csv-label", "start", "-captured-at", at.Format(time.RFC3339), "-direction", "long"}
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "scope=crosschecked_price_pattern_diagnostic") || !strings.Contains(out.String(), "crosschecked=111/111 / eligible=true") || !strings.Contains(out.String(), "trades=1") {
			t.Fatal("diagnostic output missing scope/evidence")
		}
	}
	files, _ := filepath.Glob("data/research/diagnostic-*.json")
	if len(files) != 2 {
		t.Fatal("replay overwrote report")
	}
	a, _ := os.ReadFile(files[0])
	b, _ := os.ReadFile(files[1])
	if !bytes.Equal(a, b) {
		t.Fatal("same raw inputs changed results")
	}
	manifests, _ := filepath.Glob("data/research/research-run-*.json")
	for _, f := range manifests {
		raw, _ := os.ReadFile(f)
		var m struct {
			InputSHA  string                           `json:"input_sha256"`
			ReportSHA string                           `json:"report_sha256"`
			Settings  struct{ ReferenceSHA256 string } `json:"settings"`
		}
		if json.Unmarshal(raw, &m) != nil || m.InputSHA != hash([]byte(csv.String())) || m.Settings.ReferenceSHA256 != hash(history) || m.ReportSHA != hash(a) {
			t.Fatal("two raw sources or output not traceable")
		}
	}
	// A generic normalized input must not be accepted as a broker source.
	if err := run(context.Background(), []string{"diagnose", "-input", files[0], "-reference", "ticks.json", "-symbol", "0000@1", "-csv-label", "start", "-captured-at", at.Format(time.RFC3339)}, &bytes.Buffer{}); err == nil {
		t.Fatal("diagnostic JSON input passed as raw CSV")
	}
}
