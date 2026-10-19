package marketdata

import (
	"context"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

func turnoverWindowFixture() (Dataset, DailyBar) {
	d, daily := windowFixture()
	d.Source.Format = JQuantsTurnoverJoinedFormat
	sum := new(big.Rat)
	for i := range d.Days[0].Bars {
		d.Days[0].Bars[i].TurnoverJPY = "10"
		sum.Add(sum, big.NewRat(10, 1))
	}
	d.Days[0].Events[0].TurnoverJPY = "103/20"
	sum.Add(sum, big.NewRat(103, 20))
	daily.Turnover = sum.RatString()
	return d, daily
}
func TestExtendedWindowMatchesOriginalTurnoverAndPreservesLegacyBoundary(t *testing.T) {
	d, daily := turnoverWindowFixture()
	before, _ := json.Marshal(d)
	r, err := CheckJQuantsTurnoverWindow(context.Background(), d, daily.Date, daily)
	if err != nil || !r.Eligible || r.TurnoverMatch == nil || !*r.TurnoverMatch || r.TradingEligible || r.QualityPromoted {
		t.Fatal("extended window", err)
	}
	if _, err := CheckJQuantsWindow(context.Background(), d, daily.Date, daily); err == nil {
		t.Fatal("legacy window accepted extended source")
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	daily.Turnover = "1115" // OHLCV still matches; amount differs from source.
	r, err = CheckJQuantsTurnoverWindow(context.Background(), d, daily.Date, daily)
	if err != nil || r.Eligible || !r.DailyMatch || r.TurnoverMatch == nil || *r.TurnoverMatch {
		t.Fatal("turnover mismatch promoted", err)
	}
}
func TestTurnoverContractRejectsMissingChangedSourceAndForgedBoundary(t *testing.T) {
	for _, fault := range []string{"missing", "noncanonical", "exponent", "range", "volume", "event", "duplicate", "source", "synthetic", "old_date", "exclusion"} {
		d, _ := turnoverWindowFixture()
		switch fault {
		case "missing":
			d.Days[0].Bars[0].TurnoverJPY = ""
		case "noncanonical":
			d.Days[0].Bars[0].TurnoverJPY = "10.0"
		case "exponent":
			d.Days[0].Bars[0].TurnoverJPY = "1e999999999"
		case "range":
			d.Days[0].Bars[0].TurnoverJPY = "1000000"
		case "volume":
			d.Days[0].Bars[0].Volume = nil
		case "event":
			d.Days[0].Events[0].TurnoverJPY = "5"
		case "duplicate":
			d.Days[0].Events = append(d.Days[0].Events, d.Days[0].Events[0])
		case "source":
			d.Source.Format = JQuantsJoinedFormat
		case "synthetic":
			d.Origin = "synthetic"
			d.Source.Format = "synthetic"
		case "old_date":
			d.ExcludedRows = []SourceExclusion{{Date: "2024-11-04", Rows: 1, Reason: "session_before_supported_20241105"}}
		case "exclusion":
			d.ExcludedRows = []SourceExclusion{{Date: "2024-11-01", Rows: 1, Reason: "skip_bad_result"}}
		}
		if Validate(d) == nil {
			t.Fatal("forged turnover contract accepted", fault)
		}
	}
	for _, value := range []string{"-1", "secret-marker", "1e99999", strings.Repeat("0", 129)} {
		if _, err := ParseTurnover(value); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("bad decimal admitted or exposed")
		}
	}
	r, err := ParseTurnover("1000.25")
	if err != nil || r != "4001/4" {
		t.Fatal("decimal precision")
	}
}
