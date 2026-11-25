// Package reviewstrategies compares independent price-pattern cases, not a portfolio.
// Sources, quality assessment, file I/O and strategy execution belong to callers.
package reviewstrategies

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

const Version = "fixed-cohort-review-v0.1"

type Trade struct {
	Entry int64 `json:"entry_price"`
	Exit  int64 `json:"exit_price"`
}

type Day struct {
	Symbol    string         `json:"symbol"`
	Date      string         `json:"date_jst"`
	Direction r001.Direction `json:"direction"`
	Variant   r001.Variant   `json:"variant"`
	Status    string         `json:"status"`
	Trade     *Trade         `json:"trade,omitempty"`
	Baseline  *Trade         `json:"baseline,omitempty"`
}

type Group struct {
	Direction      r001.Direction `json:"direction"`
	Variant        r001.Variant   `json:"variant"`
	CostBPS        int64          `json:"round_trip_friction_bps"`
	Cases          int            `json:"cases"`
	Eligible       int            `json:"evaluable"`
	Unevaluable    int            `json:"unevaluable"`
	NoSignal       int            `json:"no_signal"`
	Filtered       int            `json:"filtered"`
	Trades         int            `json:"trades"`
	Winners        int            `json:"winners"`
	MeanTrade      *string        `json:"mean_trade_net_bps,omitempty"`
	MedianTrade    *string        `json:"median_trade_net_bps,omitempty"`
	MeanSlot       *string        `json:"mean_evaluable_slot_net_bps,omitempty"`
	BaselineTrades int            `json:"baseline_trades"`
	BaselineMean   *string        `json:"baseline_mean_net_bps,omitempty"`
	PairedDelta    *string        `json:"mean_trade_day_delta_to_baseline_bps,omitempty"`
}

type Exclusion struct {
	Kind   string  `json:"kind"`
	Value  string  `json:"value"`
	Groups []Group `json:"groups_at_10bp"`
}

type Report struct {
	Version         string      `json:"version"`
	Scope           string      `json:"scope"`
	Dates           []string    `json:"dates"`
	Symbols         []string    `json:"symbols"`
	Groups          []Group     `json:"groups"`
	Exclusions      []Exclusion `json:"leave_one_group_out"`
	StrategyAdopted bool        `json:"strategy_adopted"`
	PortfolioMode   bool        `json:"portfolio_mode"`
	TradingEligible bool        `json:"trading_eligible"`
	UnseenHoldout   bool        `json:"unseen_holdout_verified"`
}

func dateOK(s string) bool {
	d, err := time.Parse("2006-01-02", s)
	return err == nil && d.Format("2006-01-02") == s
}

func validTrade(t *Trade) bool { return t != nil && t.Entry > 0 && t.Exit > 0 }

// Review requires the complete symbol/date/A-B/long-short grid. Missing days
// must be represented explicitly as unevaluable rather than silently dropped.
func Review(ctx context.Context, dates, symbols []string, days []Day) (Report, error) {
	r := Report{Version: Version, Scope: "independent_price_pattern_cases", Dates: append([]string{}, dates...), Symbols: append([]string{}, symbols...), Groups: []Group{}, Exclusions: []Exclusion{}}
	if len(dates) == 0 || len(dates) > 500 || len(symbols) == 0 || len(symbols) > 50 {
		return r, errors.New("比較対象の日付/銘柄数を確認してください")
	}
	sort.Strings(r.Dates)
	sort.Strings(r.Symbols)
	dateSet, symbolSet := map[string]bool{}, map[string]bool{}
	for _, d := range r.Dates {
		if !dateOK(d) || dateSet[d] {
			return r, errors.New("日付が不正または重複しています")
		}
		dateSet[d] = true
	}
	for _, s := range r.Symbols {
		if s == "" || symbolSet[s] {
			return r, errors.New("銘柄が不正または重複しています")
		}
		symbolSet[s] = true
	}
	if len(days) != len(dates)*len(symbols)*4 {
		return r, errors.New("全日付/銘柄のA/B買い/売りを不足日込みで指定してください")
	}
	type key struct {
		symbol, date string
		direction    r001.Direction
		variant      r001.Variant
	}
	seen := map[key]bool{}
	for _, d := range days {
		if ctx.Err() != nil {
			return r, errors.New("戦略比較を中断しました")
		}
		k := key{d.Symbol, d.Date, d.Direction, d.Variant}
		if !symbolSet[d.Symbol] || !dateSet[d.Date] || !r001.Valid(d.Direction) || !r001.ValidVariant(d.Variant) || seen[k] {
			return r, errors.New("比較ケースが不正または重複しています")
		}
		seen[k] = true
		switch d.Status {
		case "unevaluable":
			if d.Trade != nil || d.Baseline != nil {
				return r, errors.New("評価不能日の取引を集計できません")
			}
		case "trade", "no_signal", "filtered":
			if !validTrade(d.Baseline) || (d.Status == "trade" && !validTrade(d.Trade)) || (d.Status != "trade" && d.Trade != nil) {
				return r, errors.New("取引状態/価格/比較基準が一致しません")
			}
		default:
			return r, errors.New("未知の取引状態です")
		}
	}
	// Stable ordering also makes addition order and reports reproducible.
	ordered := append([]Day{}, days...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		return a.Direction < b.Direction
	})
	r.Groups = groups(ordered, []int64{0, 5, 10, 20})
	for _, kind := range []string{"symbol", "date"} {
		values := r.Symbols
		if kind == "date" {
			values = r.Dates
		}
		for _, value := range values {
			if ctx.Err() != nil {
				return r, errors.New("戦略比較を中断しました")
			}
			remaining := []Day{}
			for _, d := range ordered {
				if (kind == "symbol" && d.Symbol == value) || (kind == "date" && d.Date == value) {
					continue
				}
				remaining = append(remaining, d)
			}
			r.Exclusions = append(r.Exclusions, Exclusion{Kind: kind, Value: value, Groups: groups(remaining, []int64{10})})
		}
	}
	return r, nil
}

func net(t *Trade, direction r001.Direction, cost int64) *big.Rat {
	diff := t.Exit - t.Entry
	if direction == r001.Short {
		diff = -diff
	}
	r := new(big.Rat).Mul(big.NewRat(diff, t.Entry), big.NewRat(10000, 1))
	return r.Sub(r, big.NewRat(cost, 1))
}

func mean(sum *big.Rat, count int) *string {
	if count == 0 {
		return nil
	}
	s := new(big.Rat).Quo(sum, big.NewRat(int64(count), 1)).FloatString(6)
	return &s
}

func groups(days []Day, costs []int64) []Group {
	result := []Group{}
	for _, variant := range []r001.Variant{r001.A, r001.B} {
		for _, direction := range []r001.Direction{r001.Long, r001.Short} {
			for _, cost := range costs {
				g := Group{Variant: variant, Direction: direction, CostBPS: cost}
				sum, baseSum, deltaSum := new(big.Rat), new(big.Rat), new(big.Rat)
				values := []*big.Rat{}
				for _, d := range days {
					if d.Variant != variant || d.Direction != direction {
						continue
					}
					g.Cases++
					if d.Status == "unevaluable" {
						g.Unevaluable++
						continue
					}
					g.Eligible++
					if d.Status == "no_signal" {
						g.NoSignal++
					}
					if d.Status == "filtered" {
						g.Filtered++
					}
					base := net(d.Baseline, direction, cost)
					g.BaselineTrades++
					baseSum.Add(baseSum, base)
					if d.Trade == nil {
						continue
					}
					v := net(d.Trade, direction, cost)
					g.Trades++
					if v.Sign() > 0 {
						g.Winners++
					}
					sum.Add(sum, v)
					deltaSum.Add(deltaSum, new(big.Rat).Sub(v, base))
					values = append(values, v)
				}
				g.MeanTrade, g.MeanSlot = mean(sum, g.Trades), mean(sum, g.Eligible)
				g.BaselineMean, g.PairedDelta = mean(baseSum, g.BaselineTrades), mean(deltaSum, g.Trades)
				if len(values) > 0 {
					sort.Slice(values, func(i, j int) bool { return values[i].Cmp(values[j]) < 0 })
					m := new(big.Rat).Set(values[len(values)/2])
					if len(values)%2 == 0 {
						m.Add(m, values[len(values)/2-1])
						m.Quo(m, big.NewRat(2, 1))
					}
					s := m.FloatString(6)
					g.MedianTrade = &s
				}
				result = append(result, g)
			}
		}
	}
	return result
}
