package buildbars

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

func archiveFixture(rows string, count int, received string) []byte {
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "environment": "production", "symbol": "7203@1", "complete": true, "started_at": received, "history_received_at": received, "finished_at": received, "time_and_sales": map[string]any{"Symbol": "7203", "Exchange": 1, "TradingPriceCount": count, "TradingPrice": json.RawMessage(rows)}})
	return raw
}
func has(reasons []string, want string) bool {
	for _, s := range reasons {
		if s == want {
			return true
		}
	}
	return false
}

func TestNativeCSVPreservesOriginalBytesAndBoundaries(t *testing.T) {
	const rows = "2026/10/15 09:00:00,100,101,99,100.5,100\r\n2026/10/16 09:00:00,101,102,100,101.5,200\r\n2026/10/16 11:30:00,102,102,102,102,300\r\n"
	for _, header := range []string{nativeCP932Header, "日付,始値,高値,安値,終値,売買高", "\xef\xbb\xbf日付,始値,高値,安値,終値,売買高"} {
		raw := []byte(header + "\r\n" + rows)
		before := string(raw)
		d, err := FromCSV(context.Background(), raw, "7203@1", "production", "start", marketdata.Clock("2026-10-16", 12, 0))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != before || d.Source.SHA256 != sourceHash(raw) || len(d.Days) != 2 || len(d.Days[1].Bars) != 1 || len(d.Days[1].Events) != 1 {
			t.Fatal("source changed, hash lost or boundary incorrectly bucketed")
		}
		want := "kabu-chart-csv-utf8"
		if header == nativeCP932Header {
			want = "kabu-chart-csv-cp932"
		}
		if d.Source.Format != want || d.Days[0].Bars[0].FirstRow != 2 || d.Days[1].Bars[0].FirstRow != 3 || !has(d.Days[0].Issues, "csv_semantics_unverified") {
			t.Fatal("format, row provenance or quality lost")
		}
	}
	for _, raw := range []string{
		strings.Replace(nativeCP932Header, "\x93", "\x94", 1) + "\r\n" + rows,
		nativeCP932Header + "\r\n" + strings.Replace(rows, "100,", "\x81,", 1),
		"datetime,open,high,low,close,volume\n" + rows,
		"日付,始値,高値,安値,終値,売買高\n" + strings.Replace(rows, "09:00:00", "09:00:01", 1),
	} {
		if _, err := FromCSV(context.Background(), []byte(raw), "7203@1", "production", "start", marketdata.Clock("2026-10-16", 12, 0)); err == nil {
			t.Fatal("unsupported native variant accepted")
		}
	}
}
func TestHistoryKeepsRowsOrderAmbiguityAndUnfinishedMinute(t *testing.T) {
	raw := archiveFixture(`[{"Time":"2026-10-16T09:00:00+09:00","Price":100.1,"Volume":10},{"Time":"2026-10-16T00:00:00Z","Price":101,"Volume":20},{"Time":"2026-10-16T09:00:20+09:00","Price":99,"Volume":30}]`, 3, "2026-10-16T09:00:30+09:00")
	d, err := FromHistory(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Days) != 1 || len(d.Days[0].Bars) != 1 || len(d.Days[0].Issues) == 0 {
		t.Fatal("quality or timezone normalization failed")
	}
	b := d.Days[0].Bars[0]
	if b.Open != 1001000 || b.High != 1010000 || b.Low != 990000 || b.Close != 990000 || *b.Volume != 60 || b.FirstRow != 1 || b.LastRow != 3 || !has(b.Issues, "open_order_unverified") || !has(b.Issues, "unfinished_minute") {
		t.Fatalf("bar=%+v", b)
	}
	if d.Origin != "observed" || d.Source.SHA256 == "" {
		t.Fatal("source provenance missing")
	}
	backwards := strings.Replace(string(raw), `09:00:20+09:00`, `08:59:59+09:00`, 1)
	if _, err := FromHistory(context.Background(), []byte(backwards)); err == nil {
		t.Fatal("backwards input silently sorted")
	}
}
func TestBoundaryEventsAndUnknownMinutesAreNotFabricated(t *testing.T) {
	raw := archiveFixture(`[{"Time":"2026-10-16T09:00:00+09:00","Price":100,"Volume":100},{"Time":"2026-10-16T11:30:00+09:00","Price":101,"Volume":100},{"Time":"2026-10-16T12:30:00+09:00","Price":102,"Volume":100},{"Time":"2026-10-16T15:30:00+09:00","Price":103,"Volume":100}]`, 4, "2026-10-16T16:00:00+09:00")
	d, err := FromHistory(context.Background(), raw)
	if err != nil || len(d.Days[0].Bars) != 2 || len(d.Days[0].Events) != 2 {
		t.Fatalf("events=%+v err=%v", d, err)
	}
}
func TestCSVExplicitLabelsHeadersAndRejection(t *testing.T) {
	const csv = "日付,時刻,始値,高値,安値,終値,出来高\r\n2026/10/16,09:01,100.1,101,99,100.2,100\r\n"
	at := marketdata.Clock("2026-10-16", 16, 0)
	d, err := FromCSV(context.Background(), []byte("\xef\xbb\xbf"+csv), "7203@1", "production", "end", at)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Days[0].Bars[0].Start.Equal(marketdata.Clock("2026-10-16", 9, 0)) || !has(d.Days[0].Issues, "csv_semantics_unverified") {
		t.Fatal("label guessed or source blessed")
	}
	for _, input := range []struct{ data, label string }{{csv, ""}, {strings.Replace(csv, "高値", "始値", 1), "start"}, {strings.Replace(csv, "101,99", "99,101", 1), "start"}, {csv + "2026/10/16,09:01,100,101,99,100,100\r\n", "start"}, {"\xff\xfe" + csv, "start"}} {
		if _, err := FromCSV(context.Background(), []byte(input.data), "7203@1", "production", input.label, at); err == nil {
			t.Fatal("invalid CSV accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FromCSV(ctx, []byte(csv), "7203@1", "production", "start", time.Now()); err == nil {
		t.Fatal("cancellation ignored")
	}
}
