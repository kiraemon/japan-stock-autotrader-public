package buildbars

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func TestTurnoverImportKeepsActualAmountAndCountsUnsupportedNovember(t *testing.T) {
	raw := bulkHeader + "2024-11-01,15:00,00000,old,unsupported,values,retained,in,original\n2024-11-05,09:00,00000,100,102,99,101,10,1000.25\n2024-11-05,11:30,00000,100.025,100.025,100.025,100.025,20,2000.5\n"
	at := marketdata.Clock("2024-12-01", 16, 0)
	if _, err := FromJQuantsBulkCSV(context.Background(), strings.NewReader(raw), strings.Repeat("a", 64), "2024-11", at, []string{"0000@1"}); err == nil {
		t.Fatal("legacy contract silently extended")
	}
	ds, err := FromJQuantsBulkCSVWithTurnover(context.Background(), strings.NewReader(raw), strings.Repeat("a", 64), "2024-11", at, []string{"0000@1"})
	if err != nil {
		t.Fatal(err)
	}
	d := ds[0]
	if d.Source.Format != marketdata.JQuantsTurnoverFormat || d.Origin != "observed" || len(d.Days) != 1 || d.Days[0].Date != "2024-11-05" || d.Days[0].Bars[0].TurnoverJPY != "4001/4" || d.Days[0].Events[0].TurnoverJPY != "4001/2" || d.Days[0].Bars[0].FirstRow != 3 || len(d.ExcludedRows) != 1 || d.ExcludedRows[0].Rows != 1 || marketdata.Validate(d) != nil {
		t.Fatal("turnover/provenance/unsupported row accounting changed")
	}
	// HLC3*Vo and closing-price*Vo differ from this preserved exact amount.
	if d.Days[0].Bars[0].TurnoverJPY == "1010" {
		t.Fatal("Va approximated")
	}
}

func TestTurnoverImportRejectsImpossibleOrMissingAmountsAndCannotJoinLegacy(t *testing.T) {
	at := marketdata.Clock("2026-06-23", 16, 0)
	row := "2026-06-22,09:00,00000,100,102,99,101,10,1000.25\n"
	for _, amount := range []string{"", "-1", "98", "1021", "1e99999", "private-marker"} {
		raw := bulkHeader + strings.Replace(row, "1000.25", amount, 1)
		if _, err := FromJQuantsBulkCSVWithTurnover(context.Background(), strings.NewReader(raw), strings.Repeat("a", 64), "2026-06", at, []string{"0000@1"}); err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("bad turnover admitted or value leaked")
		}
	}
	modern, err := FromJQuantsBulkCSVWithTurnover(context.Background(), strings.NewReader(bulkHeader+row), strings.Repeat("a", 64), "2026-06", at, []string{"0000@1"})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := FromJQuantsBulkCSV(context.Background(), strings.NewReader(bulkHeader+row), strings.Repeat("a", 64), "2026-06", at, []string{"0000@1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := JoinJQuants(context.Background(), modern); err == nil {
		t.Fatal("modern source disguised as legacy")
	}
	if _, err := JoinJQuantsWithTurnover(context.Background(), legacy); err == nil {
		t.Fatal("missing Va inferred from OHLCV")
	}
	before, _ := json.Marshal(modern)
	joined, err := JoinJQuantsWithTurnover(context.Background(), modern)
	if err != nil || joined.Source.Format != marketdata.JQuantsTurnoverJoinedFormat || joined.Days[0].Bars[0].TurnoverJPY != "4001/4" {
		t.Fatal("extended join", err)
	}
	*joined.Days[0].Bars[0].Volume = 999
	after, _ := json.Marshal(modern)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("shared input mutated")
	}
	legacyRaw, _ := json.Marshal(legacy[0])
	if strings.Contains(string(legacyRaw), "turnover_jpy_exact") || strings.Contains(string(legacyRaw), "excluded_source_rows") {
		t.Fatal("legacy output changed")
	}
}
