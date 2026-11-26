package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-trading-lab/internal/jquantsbulk"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func writeRound3Month(t *testing.T, date, amount string) (round3MinuteFile, marketdata.Dataset) {
	t.Helper()
	dir := t.TempDir()
	month := date[:7]
	csv := "Date,Time,Code,O,H,L,C,Vo,Va\n" + date + ",09:00,00000,100,102,99,101,10," + amount + "\n"
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write([]byte(csv)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	source := compressed.Bytes()
	capture := jquantsbulk.Report{
		Month: month, Dataset: jquantsbulk.Minute, Complete: true,
		Files: []jquantsbulk.File{{Local: "source.csv.gz", SHA256: hash(source), Size: int64(len(source))}},
	}
	if err := os.WriteFile(filepath.Join(dir, capture.Files[0].Local), source, 0600); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(dir, "capture.json")
	captureRaw := writeS01FixtureJSON(t, capturePath, capture)
	const symbol = "0000@1"
	datasets, err := buildbars.FromJQuantsBulkCSVWithTurnover(context.Background(),
		strings.NewReader(csv), hash(source), month, marketdata.Clock(date, 16, 0), []string{symbol})
	if err != nil || len(datasets) != 1 {
		t.Fatalf("synthetic import: %v", err)
	}
	normalizedPath := filepath.Join(dir, "normalized.json")
	normalized := writeS01FixtureJSON(t, normalizedPath, datasets[0])
	return round3MinuteFile{
		Month: month, Symbol: symbol, Path: normalizedPath, SHA: hash(normalized),
		Capture: capturePath, CaptureSHA: hash(captureRaw), SourceSHA: hash(source), Days: 1, Bars: 1,
	}, datasets[0]
}

func TestRound3JoinVerifiesOriginalsAndPreservesExactTurnover(t *testing.T) {
	earlier, _ := writeRound3Month(t, "2026-06-22", "1000.25")
	later, _ := writeRound3Month(t, "2026-07-22", "1000.50")
	checked := map[string]string{}
	joined, err := round3Joined(context.Background(), []round3MinuteFile{later, earlier}, checked)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Source.Format != marketdata.JQuantsTurnoverJoinedFormat || len(joined.Days) != 2 || len(checked) != 2 {
		t.Fatalf("joined data or original verification incomplete: %+v", joined.Source)
	}
	for i, want := range []struct{ date, turnover string }{
		{"2026-06-22", "4001/4"}, {"2026-07-22", "2001/2"},
	} {
		day := joined.Days[i]
		if day.Date != want.date || len(day.Bars) != 1 || day.Bars[0].TurnoverJPY != want.turnover {
			t.Fatalf("monthly source order or exact turnover changed: %+v", day)
		}
	}
	repeated, err := round3Joined(context.Background(), []round3MinuteFile{earlier, later}, checked)
	if err != nil || repeated.Source.SHA256 != joined.Source.SHA256 {
		t.Fatalf("cached provenance changed joined identity: %v", err)
	}
	conflict := earlier
	conflict.Month = later.Month
	_, err = round3Joined(context.Background(), []round3MinuteFile{conflict}, checked)
	if err == nil || !strings.Contains(err.Error(), "月/hash参照") {
		t.Fatalf("same capture associated with another month: %v", err)
	}
}

func TestRound3JoinRejectsInconsistentMonthlyEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*testing.T, *round3MinuteFile, *marketdata.Dataset)
		message string
	}{
		{"original changed", func(t *testing.T, f *round3MinuteFile, _ *marketdata.Dataset) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(f.Capture), "source.csv.gz"), []byte("different bytes"), 0600); err != nil {
				t.Fatal(err)
			}
		}, "元分足gzipのhash/容量"},
		{"normalized month", func(t *testing.T, f *round3MinuteFile, d *marketdata.Dataset) {
			d.Days[0].Date = "2026-07-22"
			f.SHA = hash(writeS01FixtureJSON(t, f.Path, d))
		}, "月次分足の日付"},
		{"normalized count", func(_ *testing.T, f *round3MinuteFile, _ *marketdata.Dataset) {
			f.Bars++
		}, "月次分足の件数"},
		{"normalized source", func(t *testing.T, f *round3MinuteFile, d *marketdata.Dataset) {
			d.Source.SHA256 = strings.Repeat("0", 64)
			f.SHA = hash(writeS01FixtureJSON(t, f.Path, d))
		}, "固定した売買代金付き月次分足"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, dataset := writeRound3Month(t, "2026-06-22", "1000.25")
			tc.mutate(t, &ref, &dataset)
			result, err := round3Joined(context.Background(), []round3MinuteFile{ref}, map[string]string{})
			if err == nil || !strings.Contains(err.Error(), tc.message) || len(result.Days) != 0 {
				t.Fatalf("expected %q without joined data, got %v and %d days", tc.message, err, len(result.Days))
			}
		})
	}
}
