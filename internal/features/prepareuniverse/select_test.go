package prepareuniverse

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func selectionFixture() ([]marketdata.DailyBar, []Listing, []string) {
	sessions := []string{}
	for day := 1; day <= 30; day++ {
		at := time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			sessions = append(sessions, at.Format("2006-01-02"))
		}
	}
	daily := []marketdata.DailyBar{}
	masters := []Listing{}
	for i := 0; i < 35; i++ {
		code := fmt.Sprintf("%04d0", i)
		masters = append(masters, Listing{Date: "2026-06-30", Code: code, Market: "0111", Product: "011"})
		for _, day := range sessions {
			daily = append(daily, marketdata.DailyBar{Date: day, Code: code, HasTrades: true, Turnover: "100"})
		}
	}
	masters = append(masters, Listing{Date: "2026-06-30", Code: "88880", Market: "0111", Product: "014"}, Listing{Date: "2026-06-30", Code: "99991", Market: "0111", Product: "011"})
	return daily, masters, sessions
}
func TestSelectionUsesPreviousTwentyExactTiesHistoricalMasterAndNoMutation(t *testing.T) {
	daily, master, calendar := selectionFixture()
	before, _ := json.Marshal(daily)
	r, err := Select(context.Background(), daily, master, calendar, "2026-06-30", "2026-06-30")
	if err != nil || len(r.Days) != 1 || len(r.Days[0].Picks) != 30 || r.Days[0].Eligible != 35 || r.Days[0].Previous[0] != "2026-06-02" || r.Days[0].Picks[29].Code != "00290" || r.Days[0].Picks[0].Mean != "100" || r.QualityPromoted || r.TradingEligible || r.NewPNL {
		t.Fatal("selection or calendar incorrect", err)
	}
	after, _ := json.Marshal(daily)
	if string(before) != string(after) {
		t.Fatal("input changed")
	}
	for i := range daily {
		if daily[i].Date == "2026-06-30" || daily[i].Date == "2026-06-01" {
			daily[i].Turnover = "92233720368547758081234567890"
		}
	}
	again, err := Select(context.Background(), daily, master, calendar, "2026-06-30", "2026-06-30")
	if err != nil || !reflect.DeepEqual(r.Days, again.Days) {
		t.Fatal("target-day or old data leaked", err)
	}
	for i, j := 0, len(daily)-1; i < j; i, j = i+1, j-1 {
		daily[i], daily[j] = daily[j], daily[i]
	}
	reversed, err := Select(context.Background(), daily, master, calendar, "2026-06-30", "2026-06-30")
	if err != nil || !reflect.DeepEqual(again, reversed) {
		t.Fatal("row order affected result", err)
	}
}
func TestMissingNullFractionalAndLargeAmounts(t *testing.T) {
	daily, master, calendar := selectionFixture()
	for i := range daily {
		if daily[i].Code == "00000" && daily[i].Date == "2026-06-02" {
			daily[i].HasTrades = false
			daily[i].Turnover = ""
		}
		if daily[i].Code == "00340" {
			daily[i].Turnover = "1000/3"
		}
		if daily[i].Code == "00330" {
			daily[i].Turnover = "92233720368547758081234567890"
		}
	}
	r, err := Select(context.Background(), daily, master, calendar, "2026-06-30", "2026-06-30")
	if err != nil || r.Days[0].Picks[0].Code != "00330" || r.Days[0].Picks[0].Mean != "92233720368547758081234567890" || r.Days[0].Picks[1].Mean != "1000/3" || len(r.Days[0].Missing) != 1 || r.Days[0].Missing[0].Dates[0] != "2026-06-02" || len(r.Days[0].Previous) != 20 {
		t.Fatal("missing references skipped or arithmetic rounded", err)
	}
	master = master[:10]
	r, err = Select(context.Background(), daily, master, calendar, "2026-06-30", "2026-06-30")
	if err != nil || len(r.Days[0].Picks) != 9 || len(r.Days[0].Problems) == 0 {
		t.Fatal("short universe hidden", err)
	}
	r, err = Select(context.Background(), daily, master, calendar, "2026-06-01", "2026-06-30")
	if err != nil || len(r.Days) != 22 || r.Days[0].Problems[0] != "calendar_warmup_short" || r.Days[20].Problems[0] != "historical_master_missing" {
		t.Fatal("missing business days dropped", err)
	}
}
func TestSelectionRejectsMalformedDuplicatesAndCancellation(t *testing.T) {
	daily, master, calendar := selectionFixture()
	for _, value := range []string{"0", "-1", "1/0", "1e999999999"} {
		bad := append([]marketdata.DailyBar{}, daily...)
		bad[0].Turnover = value
		if _, err := Select(context.Background(), bad, master, calendar, "2026-06-30", "2026-06-30"); err == nil {
			t.Fatal("invalid amount admitted")
		}
	}
	if _, err := Select(context.Background(), append(daily, daily[0]), master, calendar, "2026-06-30", "2026-06-30"); err == nil {
		t.Fatal("daily duplicate admitted")
	}
	if _, err := Select(context.Background(), daily, append(master, master[0]), calendar, "2026-06-30", "2026-06-30"); err == nil {
		t.Fatal("master duplicate admitted")
	}
	if _, err := Select(context.Background(), daily, master, append(calendar, calendar[0]), "2026-06-30", "2026-06-30"); err == nil {
		t.Fatal("calendar duplicate admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Select(ctx, daily, master, calendar, "2026-06-30", "2026-06-30"); err == nil {
		t.Fatal("cancel ignored")
	}
}
