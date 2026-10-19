package prepareorb

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

func pastVolumeFixture() (marketdata.Dataset, string, []string, []marketdata.DailyBar) {
	d, date, previous := providerFixture()
	d.Days[0].Issues = []string{"coverage_unverified", "trading_status_unverified"}
	// The historical day traded, but its first trade was after the fixed 5m window.
	d.Days[0].Bars = d.Days[0].Bars[:1]
	d.Days[0].Bars[0].Start = marketdata.Clock(previous[0], 9, 6)
	return d, date, previous, fabricatedDaily(d)
}

func fabricatedDaily(d marketdata.Dataset) []marketdata.DailyBar {
	refs := []marketdata.DailyBar{}
	for _, day := range d.Days {
		r := marketdata.DailyBar{Date: day.Date, Code: "00000", HasTrades: true, AdjustmentFactor: "1"}
		for i, b := range day.Bars {
			if i == 0 {
				r.Open, r.High, r.Low = b.Open, b.High, b.Low
			}
			if b.High > r.High {
				r.High = b.High
			}
			if b.Low < r.Low {
				r.Low = b.Low
			}
			r.Close = b.Close
			if b.Volume != nil {
				r.Volume += *b.Volume
			}
		}
		for _, e := range day.Events {
			if r.Open == 0 {
				r.Open, r.High, r.Low = e.Price, e.Price, e.Price
			}
			if e.Price > r.High {
				r.High = e.Price
			}
			if e.Price < r.Low {
				r.Low = e.Price
			}
			r.Close = e.Price
			if e.Volume != nil {
				r.Volume += *e.Volume
			}
		}
		refs = append(refs, r)
	}
	return refs
}

func TestPastZeroVolumeKeeps14DatesNoPriceOrBarAndLegacyUnchanged(t *testing.T) {
	d, date, refs, daily := pastVolumeFixture()
	before, _ := json.Marshal(d)
	legacy, err := CheckWithPolicy(context.Background(), d, date, refs, JQuantsPrefixPolicy)
	if err != nil || legacy.CalculationAvailable {
		t.Fatal("legacy must retain unavailable reference", err)
	}
	evidence, err := BuildPriorZeroVolumeEvidence(context.Background(), d, daily)
	if err != nil {
		t.Fatal(err)
	}
	r, err := CheckWithPriorZeroVolume(context.Background(), d, date, refs, evidence)
	if err != nil || !r.CalculationAvailable || *r.RelativeVolume != "28/13" || *r.PreviousMean != "325/7" || len(r.Previous) != 14 || r.Previous[0].Date != refs[0] || *r.Previous[0].Volume != "0" || r.Previous[0].Range != nil || len(r.Previous[0].MissingMinutes) != 5 || len(r.Previous[0].OriginalProblems) != 1 {
		t.Fatal("wrong explicit zero volume or denominator", err)
	}
	if r.Version != PastVolumeVersion || r.OpeningPolicy != PastVolumePolicy || len(r.PriorZeroEvidence) != 1 || len(r.PriorZeroEvidence[0].DailySHA) != 64 || len(r.PriorZeroEvidence[0].DaySHA) != 64 || r.PriorZeroEvidence[0].OriginalIssues[0] != "coverage_unverified" || r.QualityPromoted || r.TradingEligible || r.StrategyAdopted {
		t.Fatal("evidence/limits lost")
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("dataset changed")
	}
	oldAgain, _ := CheckWithPolicy(context.Background(), d, date, refs, JQuantsPrefixPolicy)
	if !reflect.DeepEqual(legacy, oldAgain) {
		t.Fatal("legacy behavior changed")
	}
	r.PriorZeroEvidence[0].OriginalIssues[0] = "caller_mutation"
	again, err := CheckWithPriorZeroVolume(context.Background(), d, date, refs, evidence)
	if err != nil || again.PriorZeroEvidence[0].OriginalIssues[0] != "coverage_unverified" {
		t.Fatal("caller mutated private evidence", err)
	}
}

func TestPastZeroVolumeDoesNotAdmitTargetEmptyPartialGapOrZeroDenominator(t *testing.T) {
	for _, mode := range []string{"target", "partial", "zero_denominator", "missing_day", "no_daily"} {
		t.Run(mode, func(t *testing.T) {
			d, date, refs, daily := pastVolumeFixture()
			switch mode {
			case "target":
				d.Days[14].Bars = d.Days[14].Bars[:1]
				d.Days[14].Bars[0].Start = marketdata.Clock(date, 9, 6)
				daily = fabricatedDaily(d)
			case "partial":
				d.Days[1].Bars = d.Days[1].Bars[:4]
				daily = fabricatedDaily(d)
			case "zero_denominator":
				for i := 0; i < 14; i++ {
					d.Days[i].Bars = d.Days[i].Bars[:1]
					d.Days[i].Bars[0].Start = marketdata.Clock(refs[i], 9, 6)
				}
				daily = fabricatedDaily(d)
			case "missing_day":
				d.Days = d.Days[1:]
			case "no_daily":
				daily = daily[1:]
			}
			e, err := BuildPriorZeroVolumeEvidence(context.Background(), d, daily)
			if err != nil {
				t.Fatal(err)
			}
			r, err := CheckWithPriorZeroVolume(context.Background(), d, date, refs, e)
			if err != nil || r.CalculationAvailable || r.RelativeVolume != nil {
				t.Fatal("unsupported input admitted", err)
			}
			if mode == "target" && (r.Target.Volume != nil || r.Target.Range != nil) {
				t.Fatal("target range invented")
			}
			if mode == "zero_denominator" && (len(r.Problems) != 1 || r.Problems[0] != "zero_reference_volume") {
				t.Fatal("all-zero denominator changed")
			}
		})
	}
}

func TestPastZeroVolumeRequiresDailyMatchKnownIssuesClosedSourceAndBoundSnapshot(t *testing.T) {
	for _, mode := range []string{"daily_mismatch", "unknown_day_issue", "unknown_bar_issue", "null_volume", "unreceived", "post_build_mutation", "source_changed", "empty_evidence"} {
		t.Run(mode, func(t *testing.T) {
			d, date, refs, daily := pastVolumeFixture()
			switch mode {
			case "daily_mismatch":
				daily[0].Volume++
			case "unknown_day_issue":
				d.Days[0].Issues = append(d.Days[0].Issues, "unknown")
			case "unknown_bar_issue":
				d.Days[0].Bars[0].Issues = []string{"unknown"}
			case "null_volume":
				d.Days[0].Bars[0].Volume = nil
			case "unreceived":
				d.Source.ReceivedAt = marketdata.Clock(refs[0], 9, 6)
			}
			e, err := BuildPriorZeroVolumeEvidence(context.Background(), d, daily)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "post_build_mutation" {
				d.Days[0].Bars[0].Close++
			}
			if mode == "source_changed" {
				d.Source.SHA256 = strings.Repeat("b", 64)
			}
			if mode == "empty_evidence" {
				e = PriorZeroVolumeEvidence{}
			}
			r, err := CheckWithPriorZeroVolume(context.Background(), d, date, refs, e)
			if err == nil && r.CalculationAvailable {
				t.Fatal("unproven zero admitted")
			}
			if mode == "daily_mismatch" || mode == "unknown_day_issue" || mode == "unknown_bar_issue" || mode == "null_volume" || mode == "unreceived" {
				if err != nil || len(r.PriorZeroUnavailable) != 1 || len(r.PriorZeroUnavailable[0].Reasons) == 0 || r.PriorZeroUnavailable[0].Date != refs[0] {
					t.Fatal("failed zero assumption lost its provenance/reason", err)
				}
			}
			if (mode == "post_build_mutation" || mode == "source_changed" || mode == "empty_evidence") && err == nil {
				t.Fatal("unbound evidence not rejected")
			}
		})
	}
}

func TestPastZeroVolumeBoundaryOnlyReferenceAndNoCurrentOrFutureLeakage(t *testing.T) {
	d, date, refs, _ := pastVolumeFixture()
	v := int64(10)
	d.Days[0].Bars = nil
	d.Days[0].Events = []marketdata.Event{{Time: marketdata.Clock(refs[0], 15, 30), Price: 100000, Volume: &v}}
	daily := fabricatedDaily(d)
	e, err := BuildPriorZeroVolumeEvidence(context.Background(), d, daily)
	if err != nil {
		t.Fatal(err)
	}
	r, err := CheckWithPriorZeroVolume(context.Background(), d, date, refs, e)
	if err != nil || !r.CalculationAvailable || *r.RelativeVolume != "28/13" {
		t.Fatal("boundary-only past volume assumption failed", err)
	}
	// Whole-day target values are not used in this pre-entry volume calculation.
	daily[14].Volume++
	e2, err := BuildPriorZeroVolumeEvidence(context.Background(), d, daily)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := CheckWithPriorZeroVolume(context.Background(), d, date, refs, e2)
	if err != nil || !reflect.DeepEqual(r, r2) {
		t.Fatal("current-day daily reference leaked", err)
	}
	future := d.Days[14]
	future.Date = "2026-06-23"
	future.Bars = nil
	future.Events = nil
	d.Days = append(d.Days, future)
	r3, err := CheckWithPriorZeroVolume(context.Background(), d, date, refs, e)
	if err != nil || !reflect.DeepEqual(r, r3) {
		t.Fatal("future day leaked", err)
	}
}

func TestPastZeroVolumeRejectsWrongSourceDuplicateDailyAndCancellation(t *testing.T) {
	d, date, refs, daily := pastVolumeFixture()
	bad := d
	bad.Origin = "synthetic"
	if _, err := BuildPriorZeroVolumeEvidence(context.Background(), bad, daily); err == nil {
		t.Fatal("other origin admitted")
	}
	if _, err := BuildPriorZeroVolumeEvidence(context.Background(), d, append(daily, daily[0])); err == nil {
		t.Fatal("duplicate daily admitted")
	}
	wrong := append([]marketdata.DailyBar{}, daily...)
	wrong[0].Code = "00010"
	if _, err := BuildPriorZeroVolumeEvidence(context.Background(), d, wrong); err == nil {
		t.Fatal("wrong daily symbol admitted")
	}
	e, err := BuildPriorZeroVolumeEvidence(context.Background(), d, daily)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BuildPriorZeroVolumeEvidence(ctx, d, daily); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := CheckWithPriorZeroVolume(ctx, d, date, refs, e); err == nil {
		t.Fatal("cancel ignored in calculation")
	}
}
