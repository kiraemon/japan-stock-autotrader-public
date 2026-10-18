// Package s01 contains the fixed Japanese research variant of Stocks in Play
// ORB. Indicators never authorize an order or claim verified market quality.
package s01

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

const Version = "S01-JP20-v0.1"
const PlanSHA256 = "0f15b80ef9c7585f11a15d8053557e827d1cba0c3e490ad956c7576b37ad7e05"
const ATRPeriod = 14

var exactNumber = regexp.MustCompile(`^(0|[1-9][0-9]*)(/[1-9][0-9]*)?$`)
var symbolPattern = regexp.MustCompile(`^[0-9A-Z]{4}@1$`)

type TrueRange struct {
	Date   string `json:"date_jst"`
	Value  string `json:"true_range_scaled_exact"`
	Factor string `json:"to_previous_close_basis_exact"`
}
type ATRReport struct {
	Version         string      `json:"version"`
	Date            string      `json:"date_jst"`
	Symbol          string      `json:"symbol"`
	BasisDate       string      `json:"price_basis_date"`
	Previous        []string    `json:"previous_sessions"`
	Ranges          []TrueRange `json:"true_ranges"`
	Missing         []string    `json:"unavailable_dates"`
	ActionDates     []string    `json:"prior_corporate_action_dates"`
	Available       bool        `json:"calculation_available"`
	Value           *string     `json:"atr_scaled_exact,omitempty"`
	StopDistance    *int64      `json:"stop_distance_scaled,omitempty"`
	Problems        []string    `json:"problems"`
	QualityPromoted bool        `json:"quality_promoted"`
}

func Exact(value string) (*big.Rat, bool) {
	if len(value) == 0 || len(value) > 256 || !exactNumber.MatchString(value) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(value)
	return r, ok && r.Sign() >= 0
}
func referenceDates(date string, previous []string) bool {
	at, err := time.Parse("2006-01-02", date)
	if err != nil || at.Format("2006-01-02") != date || at.Weekday() == time.Saturday || at.Weekday() == time.Sunday || len(previous) != ATRPeriod+1 {
		return false
	}
	for i, day := range previous {
		d, err := time.Parse("2006-01-02", day)
		if err != nil || d.Format("2006-01-02") != day || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday || day >= date || (i > 0 && previous[i-1] >= day) {
			return false
		}
	}
	return true
}

// ATR uses 15 explicit PRIOR dates, producing 14 simple-mean true ranges on
// the T-1 price basis. Only recorded factors in that prior window are consulted.
func ATR(ctx context.Context, daily []marketdata.DailyBar, symbol, date string, previous []string) (ATRReport, error) {
	r := ATRReport{Version: Version, Date: date, Symbol: symbol, Previous: append([]string{}, previous...), Ranges: []TrueRange{}, Missing: []string{}, ActionDates: []string{}, Problems: []string{}}
	if !referenceDates(date, previous) || !symbolPattern.MatchString(symbol) {
		return r, errors.New("ATRの東証銘柄と直前15営業日を確認してください")
	}
	r.BasisDate = previous[len(previous)-1]
	wanted := map[string]bool{}
	for _, day := range previous {
		wanted[day] = true
	}
	byDate := map[string]marketdata.DailyBar{}
	code := strings.TrimSuffix(symbol, "@1") + "0"
	for _, d := range daily {
		if ctx.Err() != nil {
			return r, errors.New("ATR入力を中断しました")
		}
		if d.Code != code || !wanted[d.Date] {
			continue
		}
		if _, exists := byDate[d.Date]; exists {
			return r, errors.New("ATRの日足が重複しています")
		}
		byDate[d.Date] = d
	}
	factors := make([]*big.Rat, len(previous))
	prices := make([]marketdata.DailyBar, len(previous))
	for i, day := range previous {
		d, exists := byDate[day]
		if !exists || !d.HasTrades {
			r.Missing = append(r.Missing, day)
			continue
		}
		if !marketdata.ValidPrices(marketdata.Bar{Open: d.Open, High: d.High, Low: d.Low, Close: d.Close}) {
			return r, errors.New("ATRの過去OHLCを確認してください")
		}
		factor, ok := Exact(d.AdjustmentFactor)
		if !ok || factor.Sign() <= 0 {
			return r, errors.New("ATRの過去調整係数を確認してください")
		}
		prices[i], factors[i] = d, factor
		if factor.Cmp(big.NewRat(1, 1)) != 0 || d.ExRights != "" {
			r.ActionDates = append(r.ActionDates, day)
		}
	}
	if len(r.Missing) > 0 {
		r.Problems = append(r.Problems, "prior_15_daily_sessions_incomplete")
		return r, nil
	}
	scales := make([]*big.Rat, len(previous))
	scales[ATRPeriod] = big.NewRat(1, 1)
	for i := ATRPeriod - 1; i >= 0; i-- {
		scales[i] = new(big.Rat).Mul(scales[i+1], factors[i+1])
	}
	sum := new(big.Rat)
	for i := 1; i <= ATRPeriod; i++ {
		if ctx.Err() != nil {
			return r, errors.New("ATR計算を中断しました")
		}
		h := new(big.Rat).Mul(big.NewRat(prices[i].High, 1), scales[i])
		l := new(big.Rat).Mul(big.NewRat(prices[i].Low, 1), scales[i])
		prev := new(big.Rat).Mul(big.NewRat(prices[i-1].Close, 1), scales[i-1])
		tr := new(big.Rat).Sub(h, l)
		for _, value := range []*big.Rat{new(big.Rat).Abs(new(big.Rat).Sub(h, prev)), new(big.Rat).Abs(new(big.Rat).Sub(l, prev))} {
			if value.Cmp(tr) > 0 {
				tr.Set(value)
			}
		}
		sum.Add(sum, tr)
		r.Ranges = append(r.Ranges, TrueRange{previous[i], tr.RatString(), scales[i].RatString()})
	}
	atr := new(big.Rat).Quo(sum, big.NewRat(ATRPeriod, 1))
	value := atr.RatString()
	r.Value = &value
	r.Available = true
	distance := new(big.Rat).Quo(atr, big.NewRat(10, 1))
	ceil := new(big.Int).Add(distance.Num(), new(big.Int).Sub(distance.Denom(), big.NewInt(1)))
	ceil.Quo(ceil, distance.Denom())
	if !ceil.IsInt64() || ceil.Int64() > marketdata.MaxPrice {
		r.Problems = append(r.Problems, "stop_distance_out_of_range")
		return r, nil
	}
	stop := ceil.Int64()
	r.StopDistance = &stop
	return r, nil
}
