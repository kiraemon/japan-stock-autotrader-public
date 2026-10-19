// Package round4 contains two fixed research changes. It has no broker, quality
// override, portfolio or file access. Source/calendar checks belong to callers.
package round4

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

const SelectionRule = "S01-RVOL20-JP-v0.1"
const ExitRule = "NA30-VWAPEXIT-JP-v0.1"
const PlanSHA = "47336729cc17c2d102557568c699f4bcedad7f5fad99786264851081ad121f8b"

type Opening struct {
	Symbol        string `json:"symbol"`
	LiquidityRank int    `json:"liquidity_rank"`
	Available     bool   `json:"opening_available"`
	RVOL          string `json:"rvol_exact,omitempty"`
}
type Pick struct {
	Opening
	RVOLRank int  `json:"rvol_rank,omitempty"`
	Selected bool `json:"selected"`
}

// Select uses all original 30 slots, including unavailable openings. It does
// not see ATR, breakout, subsequent price or portfolio execution outcomes.
func Select(ctx context.Context, openings []Opening) ([]Pick, error) {
	if len(openings) != 30 {
		return nil, errors.New("RVOL selection requires the original 30 slots")
	}
	result := make([]Pick, 30)
	symbols := map[string]bool{}
	ranks := map[int]bool{}
	eligible := []int{}
	ratios := map[int]*big.Rat{}
	pattern := regexp.MustCompile(`^[0-9A-Z]{4}@1$`)
	for i, o := range openings {
		if ctx.Err() != nil {
			return nil, errors.New("RVOL selection interrupted")
		}
		if !pattern.MatchString(o.Symbol) || symbols[o.Symbol] || o.LiquidityRank < 1 || o.LiquidityRank > 30 || ranks[o.LiquidityRank] {
			return nil, errors.New("invalid RVOL selection identity")
		}
		symbols[o.Symbol] = true
		ranks[o.LiquidityRank] = true
		result[i].Opening = o
		if !o.Available {
			if o.RVOL != "" {
				return nil, errors.New("unavailable opening has a ratio")
			}
			continue
		}
		if len(o.RVOL) > 128 {
			return nil, errors.New("invalid RVOL ratio")
		}
		r, ok := new(big.Rat).SetString(o.RVOL)
		if !ok || r.Sign() < 0 {
			return nil, errors.New("invalid RVOL ratio")
		}
		ratios[i] = r
		if r.Cmp(big.NewRat(1, 1)) >= 0 {
			eligible = append(eligible, i)
		}
	}
	sort.Slice(eligible, func(a, b int) bool {
		i, j := eligible[a], eligible[b]
		if c := ratios[i].Cmp(ratios[j]); c != 0 {
			return c > 0
		}
		if openings[i].LiquidityRank != openings[j].LiquidityRank {
			return openings[i].LiquidityRank < openings[j].LiquidityRank
		}
		return openings[i].Symbol < openings[j].Symbol
	})
	for rank, i := range eligible {
		result[i].RVOLRank = rank + 1
		result[i].Selected = rank < 20
	}
	return result, nil
}

type Exit struct {
	At       time.Time `json:"exit_at"`
	Price    int64     `json:"exit_price_scaled"`
	SignalAt time.Time `json:"exit_signal_at"`
	VWAP     string    `json:"exit_signal_vwap_jpy_exact,omitempty"`
	Reason   string    `json:"exit_reason"`
}

// VWAPExit checks completed 09:30..09:58 bars and uses only the following
// open. After a decision it never reads that next bar's high/low/close/volume.
// It is a price rule, not proof of quote freshness or actual execution.
func VWAPExit(ctx context.Context, day marketdata.Day, direction string) (Exit, error) {
	fail := func() (Exit, error) { return Exit{}, errors.New("invalid VWAP exit price path") }
	if direction != "long" && direction != "short" || len(day.Bars) > 325 {
		return fail()
	}
	parsed, e := time.Parse("2006-01-02", day.Date)
	if e != nil || parsed.Format("2006-01-02") != day.Date || day.Date < "2024-11-05" {
		return fail()
	}
	bars := map[time.Time]marketdata.Bar{}
	for _, b := range day.Bars {
		if marketdata.Date(b.Start) != day.Date || !b.Start.Equal(b.Start.Truncate(time.Minute)) {
			return fail()
		}
		key := b.Start.UTC()
		if _, exists := bars[key]; exists {
			return fail()
		}
		bars[key] = b
	}
	sum := new(big.Rat)
	volume := new(big.Int)
	seen := false
	start := marketdata.Clock(day.Date, 9, 0)
	end := marketdata.Clock(day.Date, 10, 0)
	for at := start; at.Before(end); at = at.Add(time.Minute) {
		if ctx.Err() != nil {
			return Exit{}, errors.New("VWAP exit interrupted")
		}
		b, ok := bars[at.UTC()]
		if !ok && !seen && at.Before(start.Add(5*time.Minute)) {
			continue
		}
		if !seen && !at.Before(start.Add(5*time.Minute)) {
			return fail()
		}
		if !ok || !marketdata.ValidPrices(b) || len(b.Issues) > 0 || b.Volume == nil || *b.Volume <= 0 || !marketdata.ValidTurnover(b.TurnoverJPY, b.Volume, b.Low, b.High) {
			return fail()
		}
		seen = true
		v, _ := new(big.Rat).SetString(b.TurnoverJPY)
		sum.Add(sum, v)
		volume.Add(volume, big.NewInt(*b.Volume))
		if at.Before(marketdata.Clock(day.Date, 9, 30)) || !at.Before(marketdata.Clock(day.Date, 9, 59)) {
			continue
		}
		vwap := new(big.Rat).Quo(sum, new(big.Rat).SetInt(volume))
		cmp := big.NewRat(b.Close, marketdata.PriceScale).Cmp(vwap)
		if direction == "long" && cmp <= 0 || direction == "short" && cmp >= 0 {
			next := at.Add(time.Minute)
			fill, exists := bars[next.UTC()]
			if !exists || fill.Open <= 0 || fill.Open > marketdata.MaxPrice {
				return fail()
			}
			return Exit{At: next, Price: fill.Open, SignalAt: next, VWAP: vwap.RatString(), Reason: "completed_close_crossed_vwap_next_open"}, nil
		}
	}
	b, ok := bars[end.UTC()]
	if !ok || !seen || b.Open <= 0 || b.Open > marketdata.MaxPrice {
		return fail()
	}
	return Exit{At: end, Price: b.Open, SignalAt: end, Reason: "fixed_morning_time_exit"}, nil
}
