package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/jquantsbulk"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

func TestBulkImportChecksOriginalBytesAndGzipIntegrity(t *testing.T) {
	t.Chdir(t.TempDir())
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	_, _ = z.Write([]byte("Date,Time,Code,O,H,L,C,Vo,Va\n2026-06-22,09:00,00000,100,101,99,100,10,1000\n"))
	_ = z.Close()
	body := compressed.Bytes()
	if os.WriteFile("source.csv.gz", body, 0600) != nil {
		t.Fatal("write")
	}
	at := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	r := jquantsbulk.Report{Version: jquantsbulk.Version, Month: "2026-06", Started: at, Captured: at.Add(time.Second), Complete: true, Files: []jquantsbulk.File{{Size: int64(len(body)), Local: "source.csv.gz", SHA256: hash(body)}}}
	write := func() {
		raw, _ := json.Marshal(r)
		if os.WriteFile("capture.json", raw, 0600) != nil {
			t.Fatal("write")
		}
	}
	write()
	var out bytes.Buffer
	if err := run(context.Background(), []string{"import-jquants-bulk", "-input", "capture.json", "-symbols", "0000@1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "days=1 / bars=1 / quality_promoted=false") {
		t.Fatal("wrong summary")
	}
	files, _ := filepath.Glob("data/research/bars-*.json")
	if len(files) != 1 {
		t.Fatal("missing normalized data")
	}
	if err := run(context.Background(), []string{"import-jquants-bulk", "-input", "capture.json", "-symbols", "0000@1", "-with-turnover"}, &out); err != nil {
		t.Fatal(err)
	}
	extended, _ := filepath.Glob("data/research/bars-turnover-*.json")
	if len(extended) != 1 {
		t.Fatal("extended import not saved separately")
	}
	data, err := os.ReadFile(extended[0])
	if err != nil {
		t.Fatal(err)
	}
	var modern marketdata.Dataset
	if json.Unmarshal(data, &modern) != nil || modern.Source.Format != marketdata.JQuantsTurnoverFormat || modern.Days[0].Bars[0].TurnoverJPY != "1000" || !strings.Contains(out.String(), "excluded_selected_symbol_rows=0") {
		t.Fatal("turnover CLI lost source or amount")
	}
	for _, dataset := range []jquantsbulk.Dataset{jquantsbulk.Daily, jquantsbulk.Master} {
		r.Dataset = dataset
		write()
		if err := run(context.Background(), []string{"import-jquants-bulk", "-input", "capture.json", "-symbols", "0000@1"}, &bytes.Buffer{}); err == nil {
			t.Fatal("non-minute dataset admitted by minute importer")
		}
	}
	r.Dataset = ""
	r.Files[0].SHA256 = strings.Repeat("0", 64)
	write()
	if err := run(context.Background(), []string{"import-jquants-bulk", "-input", "capture.json", "-symbols", "0000@1"}, &bytes.Buffer{}); err == nil {
		t.Fatal("tampered original admitted")
	}
	// Even with a matching file hash, a corrupt gzip footer must fail conversion.
	body[len(body)-1] ^= 1
	_ = os.WriteFile("source.csv.gz", body, 0600)
	r.Files[0].SHA256 = hash(body)
	write()
	if err := run(context.Background(), []string{"import-jquants-bulk", "-input", "capture.json", "-symbols", "0000@1"}, &bytes.Buffer{}); err == nil {
		t.Fatal("gzip checksum ignored")
	}
}
