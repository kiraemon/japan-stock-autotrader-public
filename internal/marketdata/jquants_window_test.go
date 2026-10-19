package marketdata

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Fabricated values carry provider labels only to exercise the contract guard.
func windowFixture() (Dataset, DailyBar) {
	d := Dataset{Version: 1, PriceScale: PriceScale, Symbol: "0000@1", Environment: "production", Origin: "observed", Converter: "fabricated-test", Source: Source{Format: JQuantsJoinedFormat, Label: "start", SHA256: strings.Repeat("a", 64), ReceivedAt: Clock("2026-06-30", 16, 0)}}
	day := Day{Date: "2026-06-30", Issues: []string{"coverage_unverified", "corporate_actions_unverified"}}
	for at := Clock(day.Date, 9, 0); !at.After(Clock(day.Date, 10, 50)); at = at.Add(time.Minute) {
		v := int64(100)
		day.Bars = append(day.Bars, Bar{Start: at, Open: 1000, High: 1100, Low: 900, Close: 1050, Volume: &v})
	}
	v := int64(50)
	day.Events = []Event{{Time: Clock(day.Date, 15, 30), Price: 1030, Volume: &v}}
	d.Days = []Day{day}
	return d, DailyBar{Date: day.Date, Code: "00000", HasTrades: true, Open: 1000, High: 1100, Low: 900, Close: 1030, Volume: 11150, AdjustmentFactor: "1"}
}
func TestFixedWindowPrefixDailyMatchAndInputOwnership(t *testing.T) {
	d, daily := windowFixture()
	d.Days[0].Bars = d.Days[0].Bars[3:]
	daily.Volume -= 300
	before, _ := json.Marshal(d)
	r, err := CheckJQuantsWindow(context.Background(), d, daily.Date, daily)
	after, _ := json.Marshal(d)
	if err != nil || !r.Eligible || !r.DailyMatch || r.Observed != 108 || len(r.LeadingAssumed) != 3 || r.LeadingAssumed[2] != "09:02" || r.QualityPromoted || r.TradingEligible || r.OriginalIssues[1] != "corporate_actions_unverified" || string(before) != string(after) {
		t.Fatal("window contract or quality changed", err)
	}
	// Reconciliation is ex-post and includes auction-only days, but an auction
	// does not synthesize a morning window.
	d.Days[0].Bars = nil
	daily.Open = 1030
	daily.High = 1030
	daily.Low = 1030
	daily.Volume = 50
	r, err = CheckJQuantsWindow(context.Background(), d, daily.Date, daily)
	if err != nil || !r.DailyMatch || r.Eligible || len(r.Missing) != 111 {
		t.Fatal("boundary-only day became a window", err)
	}
}
func TestWindowRejectsMissingUnclosedActionsUnknownQualityAndMismatch(t *testing.T) {
	for _, fault := range []string{"internal", "late_first", "daily", "event", "duplicate_event", "action", "bar_issue", "day_issue", "volume", "unclosed", "unclosed_daily"} {
		d, daily := windowFixture()
		switch fault {
		case "internal":
			d.Days[0].Bars = append(d.Days[0].Bars[:10], d.Days[0].Bars[11:]...)
			daily.Volume -= 100
		case "late_first":
			d.Days[0].Bars = d.Days[0].Bars[5:]
			daily.Volume -= 500
			daily.Open = 1000
		case "daily":
			daily.Close++
		case "event":
			d.Days[0].Events[0].Time = Clock(daily.Date, 15, 25)
		case "duplicate_event":
			d.Days[0].Events = append(d.Days[0].Events, d.Days[0].Events[0])
		case "action":
			daily.AdjustmentFactor = "1/2"
		case "bar_issue":
			d.Days[0].Bars[5].Issues = []string{"unverified"}
		case "day_issue":
			d.Days[0].Issues = append(d.Days[0].Issues, "unexpected")
		case "volume":
			d.Days[0].Bars[5].Volume = nil
		case "unclosed":
			d.Source.ReceivedAt = Clock(daily.Date, 10, 50)
		case "unclosed_daily":
			d.Source.ReceivedAt = Clock(daily.Date, 11, 0) // Morning closed, auction not received.
		}
		r, err := CheckJQuantsWindow(context.Background(), d, daily.Date, daily)
		if err != nil || r.Eligible || len(r.Reasons) == 0 || r.QualityPromoted || r.TradingEligible {
			t.Fatal("unsupported input promoted", fault, err)
		}
	}
	d, daily := windowFixture()
	d.Source.Format = "kabu-timeandsales"
	if _, err := CheckJQuantsWindow(context.Background(), d, daily.Date, daily); err == nil {
		t.Fatal("other provider admitted")
	}
	d, daily = windowFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckJQuantsWindow(ctx, d, daily.Date, daily); err == nil {
		t.Fatal("cancel ignored")
	}
}
