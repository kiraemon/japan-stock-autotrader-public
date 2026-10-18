package buildbars

import (
	"context"
	"strings"
	"testing"
	"time"
)

const bulkHeader = "Date,Time,Code,O,H,L,C,Vo,Va\n"
const bulkRows = "2026-06-22,09:01,00000,100,102,99,101,10.0,1000\n2026-06-22,11:30,00000,100,100,100,100,20,2000\n2026-06-22,09:00,00000,100,102,99,101,10,1000\n"

func TestBulkCSVSelectsWithoutDisguisingSourceAndRetainsRows(t *testing.T) {
	at := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	input := bulkHeader + "2026-06-22,09:00,11110,not-selected,,,,,\n" + bulkRows
	datasets, err := FromJQuantsBulkCSV(context.Background(), strings.NewReader(input), strings.Repeat("a", 64), "2026-06", at, []string{"0000@1"})
	if err != nil {
		t.Fatal(err)
	}
	d := datasets[0]
	if len(d.Days) != 1 || len(d.Days[0].Bars) != 2 || len(d.Days[0].Events) != 1 || d.Source.Format != "jquants-bulk-minute-v0.1" || d.Source.SHA256 != strings.Repeat("a", 64) || d.Days[0].Bars[0].FirstRow != 5 || *d.Days[0].Bars[0].Volume != 10 || len(d.Days[0].Issues) == 0 {
		t.Fatal("source/order/quality changed")
	}
}

func TestBulkCSVRejectsDuplicatesInvalidSourceAndMalformedSelectedData(t *testing.T) {
	at := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	for _, input := range []string{
		"unknown,header\n", bulkHeader + bulkRows + bulkRows,
		bulkHeader + strings.Replace(bulkRows, "2026-06-22", "2026-07-22", 1),
		bulkHeader + strings.Replace(bulkRows, "10.0", "10.5", 1),
		bulkHeader + strings.Replace(bulkRows, "09:01", "12:00", 1),
		bulkHeader + strings.Replace(bulkRows, "100,102", strings.Repeat("1", 70000)+",102", 1),
	} {
		if _, err := FromJQuantsBulkCSV(context.Background(), strings.NewReader(input), strings.Repeat("a", 64), "2026-06", at, []string{"0000@1"}); err == nil {
			t.Fatal("bad input admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FromJQuantsBulkCSV(ctx, strings.NewReader(bulkHeader+bulkRows), strings.Repeat("a", 64), "2026-06", at, []string{"0000@1"}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := FromJQuantsBulkCSV(context.Background(), strings.NewReader(bulkHeader+bulkRows), "bad", "2026-06", at, []string{"0000@1"}); err == nil {
		t.Fatal("untraceable hash accepted")
	}
}
