// Package comparebars compares normalized sources without changing their quality.
package comparebars

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

const Version = "comparebars-v0.1"

type Report struct {
	Version         string `json:"version"`
	Symbol          string `json:"symbol"`
	InputSHA256     string `json:"input_source_sha256"`
	ReferenceSHA256 string `json:"reference_source_sha256"`
	QualityPromoted bool   `json:"quality_promoted"`
	Days            []Day  `json:"days"`
}

type Day struct {
	Date            string      `json:"date_jst"`
	InputBars       int         `json:"input_bars"`
	ReferenceBars   int         `json:"reference_bars"`
	Compared        int         `json:"compared_closed_minutes"`
	Unfinished      int         `json:"unfinished_overlap_minutes"`
	OnlyInput       int         `json:"minutes_only_in_input"`
	OnlyReference   int         `json:"minutes_only_in_reference"`
	OpenMatches     int         `json:"open_matches"`
	HighMatches     int         `json:"high_matches"`
	LowMatches      int         `json:"low_matches"`
	CloseMatches    int         `json:"close_matches"`
	VolumeCompared  int         `json:"volume_compared"`
	VolumeMatches   int         `json:"volume_matches"`
	AllMatches      int         `json:"ohlcv_matches"`
	MismatchMinutes []time.Time `json:"mismatch_minutes"`
}

// Compare checks only overlapping, closed regular minutes. Missing minutes,
// unknown volume, and unfinished bars never count as matches. Boundary events
// have different semantics and are deliberately outside this comparison.
func Compare(ctx context.Context, input, reference marketdata.Dataset) (Report, error) {
	for _, d := range []marketdata.Dataset{input, reference} {
		if err := marketdata.Validate(d); err != nil {
			return Report{}, err
		}
	}
	if input.Symbol != reference.Symbol || input.Environment != reference.Environment || input.Origin != reference.Origin {
		return Report{}, errors.New("照合する銘柄・市場・環境・データ種別が一致しません")
	}
	r := Report{Version: Version, Symbol: input.Symbol, InputSHA256: input.Source.SHA256, ReferenceSHA256: reference.Source.SHA256, Days: []Day{}}
	refs := map[time.Time]marketdata.Bar{}
	counts := map[string]int{}
	for _, day := range reference.Days {
		counts[day.Date] = len(day.Bars)
		for _, bar := range day.Bars {
			refs[bar.Start.UTC()] = bar
		}
	}
	seen := map[string]bool{}
	for _, day := range input.Days {
		result := Day{Date: day.Date, InputBars: len(day.Bars), ReferenceBars: counts[day.Date], OnlyReference: counts[day.Date], MismatchMinutes: []time.Time{}}
		seen[day.Date] = true
		for _, b := range day.Bars {
			if ctx.Err() != nil {
				return Report{}, errors.New("分足照合を中断しました")
			}
			other, ok := refs[b.Start.UTC()]
			if !ok {
				result.OnlyInput++
				continue
			}
			result.OnlyReference--
			if b.Start.Add(time.Minute).After(input.Source.ReceivedAt) || other.Start.Add(time.Minute).After(reference.Source.ReceivedAt) || unfinished(b) || unfinished(other) {
				result.Unfinished++
				continue
			}
			result.Compared++
			matches := 0
			for _, pair := range []struct {
				equal bool
				count *int
			}{
				{b.Open == other.Open, &result.OpenMatches}, {b.High == other.High, &result.HighMatches},
				{b.Low == other.Low, &result.LowMatches}, {b.Close == other.Close, &result.CloseMatches},
			} {
				if pair.equal {
					*pair.count++
					matches++
				}
			}
			if b.Volume != nil && other.Volume != nil {
				result.VolumeCompared++
				if *b.Volume == *other.Volume {
					result.VolumeMatches++
					matches++
				}
			}
			if matches == 5 {
				result.AllMatches++
			} else {
				result.MismatchMinutes = append(result.MismatchMinutes, b.Start)
			}
		}
		r.Days = append(r.Days, result)
	}
	for _, day := range reference.Days {
		if !seen[day.Date] {
			r.Days = append(r.Days, Day{Date: day.Date, ReferenceBars: len(day.Bars), OnlyReference: len(day.Bars), MismatchMinutes: []time.Time{}})
		}
	}
	if ctx.Err() != nil {
		return Report{}, errors.New("分足照合を中断しました")
	}
	sort.Slice(r.Days, func(i, j int) bool { return r.Days[i].Date < r.Days[j].Date })
	return r, nil
}

func unfinished(b marketdata.Bar) bool {
	for _, issue := range b.Issues {
		if issue == "unfinished_minute" {
			return true
		}
	}
	return false
}
