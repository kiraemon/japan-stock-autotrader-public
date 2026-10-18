package prepareuniverse

import (
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
)

const tradedRow = "2026-06-02,00000,100,103,99,101,0,0,100,10050.5,0.5,,1\n"

func TestDailyNumbersNullSemanticsAndCorporateActionFields(t *testing.T) {
	rows, err := ReadDaily(context.Background(), strings.NewReader(dailyHeader+"\n"+tradedRow+"2026-06-01,00000,,,,,0,0,,,1,,\n"), "2026-06")
	if err != nil || len(rows) != 2 {
		t.Fatal("daily parse failed", err)
	}
	if !rows[0].HasTrades || rows[0].Turnover != "20101/2" || rows[0].AdjustmentFactor != "1/2" || rows[0].ExRights != "1" || rows[0].Open != 1000000 || rows[0].Volume != 100 {
		t.Fatal("daily precision or fields lost")
	}
	if rows[1].HasTrades || rows[1].Turnover != "" || rows[1].Open != 0 || rows[1].Volume != 0 {
		t.Fatal("no-trade data invented")
	}
}
func TestDailyMalformedPartialNullAndCorruptGzipFailWithoutValues(t *testing.T) {
	for _, body := range []string{
		tradedRow + tradedRow,
		strings.Replace(tradedRow, "2026-06-02", "2026-05-02", 1),
		strings.Replace(tradedRow, "2026-06-02", "2026-06-31", 1),
		strings.Replace(tradedRow, ",100,103,", ",,103,", 1),
		strings.Replace(tradedRow, ",100,10050.5,", ",0,10050.5,", 1),
		strings.Replace(tradedRow, ",100,10050.5,", ",1.5,10050.5,", 1),
		strings.Replace(tradedRow, ",10050.5,", ",1e999999,", 1),
		strings.Replace(tradedRow, ",10050.5,", ",150.5,", 1),
		strings.Replace(tradedRow, ",0.5,", ",0,", 1),
		strings.Replace(tradedRow, ",103,", ",98,", 1),
		strings.Replace(tradedRow, "00000", "secret-marker", 1),
		strings.Replace(tradedRow, ",,1\n", ",,9\n", 1),
		strings.Repeat("x", (64<<10)+1),
	} {
		if _, err := ReadDaily(context.Background(), strings.NewReader(dailyHeader+"\n"+body), "2026-06"); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("bad or private input admitted")
		}
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	_, _ = z.Write([]byte(dailyHeader + "\n" + tradedRow))
	_ = z.Close()
	raw := buf.Bytes()
	raw[len(raw)-1] ^= 1
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := ReadDaily(context.Background(), reader, "2026-06"); err == nil {
		t.Fatal("gzip footer ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadDaily(ctx, strings.NewReader(dailyHeader+"\n"+tradedRow), "2026-06"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestMasterQuotedFieldsAndExactCodeClassificationInputs(t *testing.T) {
	body := masterHeader + "\n2026-06-02,186A0,\"company, name\",\"quoted\nname\",1,a,2,b,none,0111,Prime,2,loan,011\n"
	rows, err := ReadListings(context.Background(), strings.NewReader(body), "2026-06")
	if err != nil || len(rows) != 1 || rows[0].Code != "186A0" || rows[0].Market != "0111" || rows[0].Product != "011" {
		t.Fatal("quoted master failed", err)
	}
	for _, bad := range []string{body + strings.SplitN(body, "\n", 2)[1], strings.Replace(body, "ProdCat", "Product", 1), strings.Replace(body, "186A0", "186A", 1)} {
		if _, err := ReadListings(context.Background(), strings.NewReader(bad), "2026-06"); err == nil {
			t.Fatal("invalid master admitted")
		}
	}
}
