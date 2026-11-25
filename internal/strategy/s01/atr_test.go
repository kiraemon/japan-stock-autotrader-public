package s01

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func atrFixture() ([]marketdata.DailyBar, string, []string) {
	date := "2026-06-30"
	previous := []string{}
	for at := marketdata.Clock(date, 0, 0).AddDate(0, 0, -1); len(previous) < 15; at = at.AddDate(0, 0, -1) {
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			previous = append([]string{marketdata.Date(at)}, previous...)
		}
	}
	rows := []marketdata.DailyBar{}
	for _, day := range previous {
		rows = append(rows, marketdata.DailyBar{Date: day, Code: "00000", HasTrades: true, Open: 1000000, High: 1020000, Low: 980000, Close: 1000000, AdjustmentFactor: "1"})
	}
	return rows, date, previous
}
func TestATRExactSimpleMeanCeilAndFutureDataIgnored(t *testing.T) {
	rows, date, previous := atrFixture()
	rows[14].High++
	before, _ := json.Marshal(rows)
	r, err := ATR(context.Background(), rows, "0000@1", date, previous)
	if err != nil || !r.Available || *r.Value != "560001/14" || *r.StopDistance != 4001 || len(r.Ranges) != 14 || r.QualityPromoted {
		t.Fatal("ATR mean or ceiling incorrect", err)
	}
	after, _ := json.Marshal(rows)
	if string(before) != string(after) {
		t.Fatal("daily input mutated")
	}
	rows = append(rows, marketdata.DailyBar{Date: date, Code: "00000", HasTrades: true, High: 999999999999, AdjustmentFactor: "invalid-future-value"})
	again, err := ATR(context.Background(), rows, "0000@1", date, previous)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("current-day price/factor leaked", err)
	}
}
func TestATRSplitAndReverseSplitUseOnlyPriorKnownPriceBasis(t *testing.T) {
	for _, factor := range []string{"1/2", "2"} {
		rows, date, previous := atrFixture()
		for i := 7; i < 15; i++ {
			if factor == "1/2" {
				rows[i].Open /= 2
				rows[i].High /= 2
				rows[i].Low /= 2
				rows[i].Close /= 2
			} else {
				rows[i].Open *= 2
				rows[i].High *= 2
				rows[i].Low *= 2
				rows[i].Close *= 2
			}
		}
		rows[7].AdjustmentFactor = factor
		r, err := ATR(context.Background(), rows, "0000@1", date, previous)
		want := "20000"
		distance := int64(2000)
		if factor == "2" {
			want = "80000"
			distance = 8000
		}
		if err != nil || !r.Available || *r.Value != want || *r.StopDistance != distance || len(r.ActionDates) != 1 {
			t.Fatal("corporate action created false TR gap", err)
		}
	}
}
func TestATRMissingReferencesZeroAndMalformedInput(t *testing.T) {
	rows, date, previous := atrFixture()
	rows[3].HasTrades = false
	r, err := ATR(context.Background(), rows, "0000@1", date, previous)
	if err != nil || r.Available || len(r.Missing) != 1 || r.Missing[0] != previous[3] || r.Value != nil {
		t.Fatal("missing day filled or skipped", err)
	}
	rows, date, previous = atrFixture()
	for i := range rows {
		rows[i].High = rows[i].Open
		rows[i].Low = rows[i].Open
	}
	r, err = ATR(context.Background(), rows, "0000@1", date, previous)
	if err != nil || !r.Available || *r.Value != "0" || *r.StopDistance != 0 {
		t.Fatal("known zero ATR changed", err)
	}
	if _, err := ATR(context.Background(), append(rows, rows[0]), "0000@1", date, previous); err == nil {
		t.Fatal("duplicate prior day admitted")
	}
	for _, value := range []string{"0", "1e999999999", "-1", "1/0"} {
		bad := append([]marketdata.DailyBar{}, rows...)
		bad[2].AdjustmentFactor = value
		if _, err := ATR(context.Background(), bad, "0000@1", date, previous); err == nil {
			t.Fatal("bad factor admitted")
		}
	}
	if _, err := ATR(context.Background(), rows, "0000@1", date, previous[:14]); err == nil {
		t.Fatal("short calendar accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ATR(ctx, rows, "0000@1", date, previous); err == nil {
		t.Fatal("cancellation ignored")
	}
}
