// Package prepareorb diagnoses the opening-volume inputs for Stocks in Play.
// It does not select a universe, generate trades, or promote source quality.
package prepareorb

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

const Version = "stocks-in-play-input-v0.1"
const Lookback = 14

type Range struct {
	Open  int64 `json:"open"`
	High  int64 `json:"high"`
	Low   int64 `json:"low"`
	Close int64 `json:"close"`
}

type Opening struct {
	Date           string   `json:"date_jst"`
	MissingMinutes []string `json:"missing_minutes_jst"`
	Problems       []string `json:"problems"`
	SourceIssues   []string `json:"source_day_issues"`
	Volume         *string  `json:"volume_shares,omitempty"`
	Range          *Range   `json:"range_scaled,omitempty"`
}

type Report struct {
	Version              string            `json:"version"`
	Scope                string            `json:"scope"`
	Symbol               string            `json:"symbol"`
	Origin               string            `json:"origin"`
	Source               marketdata.Source `json:"source"`
	PriceScale           int64             `json:"price_scale"`
	AsOf                 time.Time         `json:"as_of_jst"`
	Target               Opening           `json:"target"`
	Previous             []Opening         `json:"previous_sessions"`
	CompletePrevious     int               `json:"complete_previous_openings"`
	MissingPrevious      []string          `json:"unavailable_previous_dates"`
	CalculationAvailable bool              `json:"calculation_available"`
	PreviousMean         *string           `json:"previous_mean_volume_exact,omitempty"`
	RelativeVolume       *string           `json:"relative_volume_exact,omitempty"`
	AtLeastOne           *bool             `json:"relative_volume_at_least_one,omitempty"`
	Problems             []string          `json:"problems"`
	QualityPromoted      bool              `json:"quality_promoted"`
	TradingEligible      bool              `json:"trading_eligible"`
	StrategyAdopted      bool              `json:"strategy_adopted"`
}

// ValidateSessions requires an explicit calendar. Observed rows must never
// determine which missing dates get skipped from the reference window.
func ValidateSessions(date string, previous []string) error {
	valid := func(s string) bool {
		d, err := time.Parse("2006-01-02", s)
		return err == nil && d.Format("2006-01-02") == s && d.Weekday() != time.Saturday && d.Weekday() != time.Sunday
	}
	if !valid(date) || len(previous) != Lookback {
		return errors.New("評価日と直前14営業日の明示した日付を確認してください")
	}
	for i, day := range previous {
		if !valid(day) || day >= date || (i > 0 && previous[i-1] >= day) {
			return errors.New("参照日は評価日より前の重複しない昇順14日で指定してください")
		}
	}
	return nil
}

func opening(d marketdata.Dataset, date string) Opening {
	o := Opening{Date: date, MissingMinutes: []string{}, Problems: []string{}, SourceIssues: []string{}}
	var day *marketdata.Day
	for i := range d.Days {
		if d.Days[i].Date == date {
			day = &d.Days[i]
			break
		}
	}
	byTime := map[time.Time]marketdata.Bar{}
	if day != nil {
		o.SourceIssues = append(o.SourceIssues, day.Issues...)
		for _, b := range day.Bars {
			byTime[b.Start.UTC()] = b
		}
	}
	total := new(big.Int)
	rangeValue := Range{}
	for minute := 0; minute < 5; minute++ {
		at := marketdata.Clock(date, 9, minute)
		b, ok := byTime[at.UTC()]
		if !ok {
			o.MissingMinutes = append(o.MissingMinutes, at.Format("15:04"))
			continue
		}
		if b.Volume == nil {
			o.Problems = append(o.Problems, "missing_volume_"+at.Format("15:04"))
		} else {
			total.Add(total, big.NewInt(*b.Volume))
		}
		if len(b.Issues) > 0 {
			o.Problems = append(o.Problems, "bar_issues_"+at.Format("15:04"))
		}
		if d.Source.ReceivedAt.Before(at.Add(time.Minute)) {
			o.Problems = append(o.Problems, "unclosed_minute_"+at.Format("15:04"))
		}
		if minute == 0 {
			rangeValue.Open, rangeValue.High, rangeValue.Low = b.Open, b.High, b.Low
		}
		if b.High > rangeValue.High {
			rangeValue.High = b.High
		}
		if rangeValue.Low == 0 || b.Low < rangeValue.Low {
			rangeValue.Low = b.Low
		}
		if minute == 4 {
			rangeValue.Close = b.Close
		}
	}
	if len(o.MissingMinutes) > 0 {
		o.Problems = append(o.Problems, "missing_opening_minutes")
	}
	if len(o.Problems) == 0 {
		value := total.String()
		o.Volume, o.Range = &value, &rangeValue
	}
	return o
}

func Check(ctx context.Context, d marketdata.Dataset, date string, previous []string) (Report, error) {
	r := Report{Version: Version, Scope: "opening_volume_input_diagnostic_only", Symbol: d.Symbol, Origin: d.Origin, Source: d.Source, PriceScale: marketdata.PriceScale, AsOf: marketdata.Clock(date, 9, 5), Previous: []Opening{}, MissingPrevious: []string{}, Problems: []string{}}
	if err := ValidateSessions(date, previous); err != nil {
		return r, err
	}
	if err := marketdata.Validate(d); err != nil {
		return r, err
	}
	if !strings.HasSuffix(d.Symbol, "@1") {
		return r, errors.New("寄り付き検査は東証の銘柄に限定しています")
	}
	if ctx.Err() != nil {
		return r, errors.New("ORB入力検査を中断しました")
	}
	r.Target = opening(d, date)
	total := new(big.Int)
	for _, day := range previous {
		if ctx.Err() != nil {
			return r, errors.New("ORB入力検査を中断しました")
		}
		o := opening(d, day)
		r.Previous = append(r.Previous, o)
		if o.Volume == nil {
			r.MissingPrevious = append(r.MissingPrevious, day)
		} else {
			r.CompletePrevious++
			v, _ := new(big.Int).SetString(*o.Volume, 10)
			total.Add(total, v)
		}
	}
	if r.Target.Volume == nil {
		r.Problems = append(r.Problems, "target_opening_unavailable")
	}
	if r.CompletePrevious != Lookback {
		r.Problems = append(r.Problems, "previous_14_openings_incomplete")
	}
	if len(r.Problems) > 0 {
		return r, nil
	}
	if total.Sign() == 0 {
		r.Problems = append(r.Problems, "zero_reference_volume")
		return r, nil
	}
	mean := new(big.Rat).SetFrac(total, big.NewInt(Lookback)).RatString()
	target, _ := new(big.Int).SetString(*r.Target.Volume, 10)
	numerator := new(big.Int).Mul(target, big.NewInt(Lookback))
	ratio := new(big.Rat).SetFrac(numerator, total).RatString()
	atLeast := numerator.Cmp(total) >= 0
	r.CalculationAvailable, r.PreviousMean, r.RelativeVolume, r.AtLeastOne = true, &mean, &ratio, &atLeast
	return r, nil
}
