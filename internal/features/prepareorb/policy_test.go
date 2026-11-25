package prepareorb

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

// These are fabricated bars. A provider label exercises the contract guard; it
// does not assert that the synthetic fixture was received from the provider.
func providerFixture() (marketdata.Dataset, string, []string) {
	d, date, previous := fixture()
	d.Origin, d.Environment = "observed", "production"
	d.Source.Format = "jquants-bulk-minute-v0.1"
	d.Days[0].Issues = []string{"source_coverage_unverified"}
	return d, date, previous
}

func TestPrefixPolicyPreservesClockRangeDenominatorAndStrictDefault(t *testing.T) {
	d, date, previous := providerFixture()
	d.Days[0].Bars = d.Days[0].Bars[3:]   // Reference volume 20 instead of 50.
	d.Days[14].Bars = d.Days[14].Bars[1:] // Target volume 80 instead of 100.
	before, _ := json.Marshal(d)
	strict, err := Check(context.Background(), d, date, previous)
	if err != nil || strict.CalculationAvailable || strict.Version != Version || strict.AvailablePrevious != nil {
		t.Fatal("strict default changed", err)
	}
	r, err := CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy)
	if err != nil || !r.CalculationAvailable || *r.RelativeVolume != "112/67" || *r.PreviousMean != "335/7" || *r.Target.Volume != "80" || r.CompletePrevious != 13 || *r.AvailablePrevious != 14 || len(r.Previous) != 14 {
		t.Fatal("wrong fixed-clock aggregation", err)
	}
	if r.Version != PolicyVersion || !r.SourceContractAssumed || r.Target.Range.Open != d.Days[14].Bars[0].Open || r.Target.Range.Close != 100004 || len(r.Target.MissingMinutes) != 1 || len(r.Previous[0].MissingMinutes) != 3 || r.Previous[0].MissingInterpretation == "" || r.Previous[0].SourceIssues[0] != "source_coverage_unverified" || r.TradingEligible || r.StrategyAdopted || r.QualityPromoted {
		t.Fatal("provenance or assumption lost")
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("policy invented or mutated bars")
	}
	v := int64(900000)
	d.Days[14].Bars = append(d.Days[14].Bars, marketdata.Bar{Start: marketdata.Clock(date, 9, 5), Open: 150000, High: 180000, Low: 140000, Close: 170000, Volume: &v})
	again, err := CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("window shifted or future leaked", err)
	}
}

func TestPrefixPolicyKeepsUnknownMissingInvalidAndUnclosedInputsUnavailable(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(*marketdata.Day)
	}{
		{"whole_day", func(day *marketdata.Day) { day.Bars = nil }},
		{"internal", func(day *marketdata.Day) { day.Bars = append(day.Bars[:2], day.Bars[3:]...) }},
		{"trailing", func(day *marketdata.Day) { day.Bars = day.Bars[:4] }},
		{"null_volume", func(day *marketdata.Day) { day.Bars = day.Bars[3:]; day.Bars[0].Volume = nil }},
		{"bar_issue", func(day *marketdata.Day) { day.Bars = day.Bars[3:]; day.Bars[0].Issues = []string{"unknown"} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, date, previous := providerFixture()
			c.change(&d.Days[0])
			r, err := CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy)
			if err != nil || r.CalculationAvailable || r.RelativeVolume != nil || *r.AvailablePrevious != 13 || len(r.MissingPrevious) != 1 || r.MissingPrevious[0] != previous[0] {
				t.Fatal("invalid reference skipped or admitted", err)
			}
		})
	}
	d, date, previous := providerFixture()
	d.Days[14].Bars = d.Days[14].Bars[3:]
	d.Source.ReceivedAt = marketdata.Clock(date, 9, 4)
	r, err := CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy)
	if err != nil || r.CalculationAvailable || r.Target.Volume != nil {
		t.Fatal("unfinished opening admitted", err)
	}
	d, date, previous = providerFixture()
	d.Days[14].Bars = nil
	v := int64(10)
	d.Days[14].Bars = []marketdata.Bar{{Start: marketdata.Clock(date, 9, 5), Open: 100, High: 101, Low: 99, Close: 100, Volume: &v}}
	r, err = CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy)
	if err != nil || r.CalculationAvailable || r.Target.Volume != nil || len(r.Target.MissingMinutes) != 5 {
		t.Fatal("post-09:05 opening shifted into window", err)
	}
}

func TestPrefixPolicyRejectsOtherProvidersOriginsEnvironmentsAndUnknownPolicies(t *testing.T) {
	for _, change := range []func(*marketdata.Dataset){
		func(d *marketdata.Dataset) { d.Source.Format = "chart-csv" },
		func(d *marketdata.Dataset) { d.Source.Format = "jquants-cli-minute-v0.1" },
		func(d *marketdata.Dataset) { d.Origin = "synthetic"; d.Source.Format = "synthetic" },
		func(d *marketdata.Dataset) { d.Environment = "test" },
		func(d *marketdata.Dataset) { d.Source.Label = "end" },
	} {
		d, date, previous := providerFixture()
		change(&d)
		if _, err := CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy); err == nil {
			t.Fatal("unsupported source admitted")
		}
	}
	d, date, previous := providerFixture()
	if _, err := CheckWithPolicy(context.Background(), d, date, previous, "zero-fill"); err == nil {
		t.Fatal("unknown policy admitted")
	}
}

func TestJoinedBulkPolicyKeepsOpeningResultAndQuality(t *testing.T) {
	d, date, previous := providerFixture()
	original, err := CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy)
	if err != nil {
		t.Fatal(err)
	}
	d.Source.Format = marketdata.JQuantsJoinedFormat
	joined, err := CheckWithPolicy(context.Background(), d, date, previous, JQuantsPrefixPolicy)
	if err != nil {
		t.Fatal(err)
	}
	original.Source.Format = marketdata.JQuantsJoinedFormat
	if !reflect.DeepEqual(original, joined) || joined.QualityPromoted || joined.TradingEligible {
		t.Fatal("joining changed calculation or promoted quality")
	}
}
