// Package backtest replays completed research bars. It has no broker, wallet,
// credential, network, or real-order interface.
package backtest

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

const ModelVersion = "next-minute-open-v0.1"

type Trade struct {
	SignalAt   time.Time `json:"signal_at"`
	EntryAt    time.Time `json:"entry_at"`
	ExitAt     time.Time `json:"exit_at"`
	EntryPrice int64     `json:"entry_price"`
	ExitPrice  int64     `json:"exit_price"`
	Reason     string    `json:"exit_reason"`
	GrossBPS   string    `json:"gross_return_bps"`
}
type Result struct {
	Date           string     `json:"date_jst"`
	Status         string     `json:"status"`
	Reasons        []string   `json:"reasons"`
	Trade          *Trade     `json:"trade,omitempty"`
	Baseline       *Trade     `json:"baseline,omitempty"`
	BaselineReason string     `json:"baseline_reason,omitempty"`
	CandidateAt    *time.Time `json:"candidate_at,omitempty"`
}
type Cost struct {
	RoundTripBPS int64   `json:"round_trip_bps"`
	Trades       int     `json:"trades"`
	Winners      int     `json:"winners"`
	MeanBPS      *string `json:"mean_net_return_bps,omitempty"`
	MedianBPS    *string `json:"median_net_return_bps,omitempty"`
}
type Report struct {
	Version                   int              `json:"report_version"`
	Rule                      string           `json:"strategy_version"`
	Model                     string           `json:"execution_model_version"`
	Direction                 r001.Direction   `json:"direction"`
	Symbol                    string           `json:"symbol"`
	PriceScale                int64            `json:"price_scale"`
	Scope                     string           `json:"scope"`
	SourceSHA256              string           `json:"source_sha256"`
	StrategyAdopted           bool             `json:"strategy_adopted"`
	PortfolioMode             bool             `json:"portfolio_mode"`
	ShortAvailabilityVerified bool             `json:"short_availability_verified"`
	Days                      []Result         `json:"days"`
	Costs                     []Cost           `json:"cost_sensitivity"`
	BaselineCosts             []Cost           `json:"baseline_cost_sensitivity"`
	Diagnostic                *PriceDiagnostic `json:"price_diagnostic,omitempty"`
}

func Run(ctx context.Context, input marketdata.Dataset, direction r001.Direction) (Report, error) {
	return run(ctx, input, direction, nil)
}

// priceOnly is private to Diagnose. Normal Run never accepts a quality override.
func run(ctx context.Context, input marketdata.Dataset, direction r001.Direction, priceOnly map[string][]string) (Report, error) {
	r := Report{Version: 1, Rule: r001.Version(direction), Model: ModelVersion, Direction: direction, Symbol: input.Symbol, PriceScale: marketdata.PriceScale, SourceSHA256: input.Source.SHA256, Scope: "price_pattern_only", Days: []Result{}}
	if input.Origin == "synthetic" {
		r.Scope = "synthetic_logic_test"
	}
	if !r001.Valid(direction) {
		return r, errors.New("directionはlongまたはshortを指定してください")
	}
	if err := marketdata.Validate(input); err != nil {
		return r, err
	}
	for _, day := range input.Days {
		if ctx.Err() != nil {
			return r, errors.New("バックテストを中断しました")
		}
		result := Result{Date: day.Date, Reasons: []string{}}
		if priceOnly != nil {
			reasons, assessed := priceOnly[day.Date]
			if !assessed {
				reasons = []string{"diagnostic_evidence_missing"}
			}
			// This local day is a value copy; original quality stays on the
			// input and in the diagnostic evidence, never promoted to verified.
			day.Issues = reasons
		} else if input.Origin == "observed" && len(day.Issues) == 0 {
			// Missing quality fields must not turn observed input into eligible data.
			day.Issues = []string{"real_data_verification_pending"}
		}
		if len(day.Issues) > 0 {
			result.Status = "unevaluable"
			result.Reasons = append(result.Reasons, day.Issues...)
			result.BaselineReason = "day_quality_unverified"
			r.Days = append(r.Days, result)
			continue
		}
		bars := map[time.Time]marketdata.Bar{}
		for _, b := range day.Bars {
			bars[b.Start.UTC()] = b
		}
		result.Baseline, result.BaselineReason = baseline(bars, day.Date, direction)
		boundary := int64(0)
		if direction == r001.Short {
			boundary = marketdata.MaxPrice
		}
		start := marketdata.Clock(day.Date, 9, 0)
		var previous int64
		failed := ""
		for minute := 0; minute < 15; minute++ {
			b, reason := usable(bars, start.Add(time.Duration(minute)*time.Minute))
			if reason != "" {
				failed = reason
				break
			}
			if direction == r001.Long && b.High > boundary {
				boundary = b.High
			}
			if direction == r001.Short && b.Low < boundary {
				boundary = b.Low
			}
			previous = b.Close
		}
		if failed != "" {
			result.Status = "unevaluable"
			result.Reasons = append(result.Reasons, "initial_range:"+failed)
			r.Days = append(r.Days, result)
			continue
		}
		result.Status = "no_signal"
		for at := start.Add(15 * time.Minute); r001.SignalWindow(at); at = at.Add(time.Minute) {
			b, reason := usable(bars, at)
			if reason != "" {
				result.Status = "unevaluable"
				result.Reasons = append(result.Reasons, "signal_window:"+reason)
				break
			}
			if r001.Crossed(direction, previous, b.Close, boundary) {
				signalAt := at.Add(time.Minute)
				result.CandidateAt = &signalAt
				trade, reason := execute(bars, signalAt, boundary, direction)
				if reason != "" {
					result.Status = "unevaluable"
					result.Reasons = append(result.Reasons, reason)
				} else {
					result.Status = "trade"
					result.Trade = trade
				}
				break
			}
			previous = b.Close
		}
		r.Days = append(r.Days, result)
	}
	for _, bps := range []int64{0, 5, 10, 20} {
		r.Costs = append(r.Costs, aggregate(r.Days, bps, false, direction))
		r.BaselineCosts = append(r.BaselineCosts, aggregate(r.Days, bps, true, direction))
	}
	return r, nil
}

func usable(bars map[time.Time]marketdata.Bar, at time.Time) (marketdata.Bar, string) {
	b, ok := bars[at.UTC()]
	if !ok {
		return b, "required_minute_missing"
	}
	if len(b.Issues) > 0 {
		return b, "minute_quality_unverified"
	}
	return b, ""
}
func execute(bars map[time.Time]marketdata.Bar, entryAt time.Time, boundary int64, d r001.Direction) (*Trade, string) {
	entry, reason := usable(bars, entryAt)
	if reason != "" {
		return nil, "entry:" + reason
	}
	deadline := entryAt.Add(r001.MaximumHold)
	for at := entryAt; !at.After(deadline); at = at.Add(time.Minute) {
		b, reason := usable(bars, at)
		if reason != "" {
			return nil, "exit:" + reason
		}
		if at.Equal(deadline) {
			return trade(entryAt, entryAt, at, entry.Open, b.Open, "maximum_hold", d), ""
		}
		if r001.Reversed(d, b.Close, boundary) {
			next := at.Add(time.Minute)
			if next.Equal(deadline) {
				continue
			}
			exit, reason := usable(bars, next)
			if reason != "" {
				return nil, "exit:" + reason
			}
			return trade(entryAt, entryAt, next, entry.Open, exit.Open, "range_reversal", d), ""
		}
	}
	return nil, "exit_unavailable"
}
func baseline(bars map[time.Time]marketdata.Bar, day string, d r001.Direction) (*Trade, string) {
	entryAt, exitAt := marketdata.Clock(day, 9, 30), marketdata.Clock(day, 9, 50)
	entry, reason := usable(bars, entryAt)
	if reason != "" {
		return nil, "baseline_entry:" + reason
	}
	exit, reason := usable(bars, exitAt)
	if reason != "" {
		return nil, "baseline_exit:" + reason
	}
	return trade(entryAt, entryAt, exitAt, entry.Open, exit.Open, "fixed_time_baseline", d), ""
}
func gross(entry, exit int64, d r001.Direction) *big.Rat {
	diff := exit - entry
	if d == r001.Short {
		diff = -diff
	}
	return new(big.Rat).Mul(big.NewRat(diff, entry), big.NewRat(10000, 1))
}
func trade(signal, entry, exit time.Time, entryPrice, exitPrice int64, reason string, d r001.Direction) *Trade {
	return &Trade{SignalAt: signal, EntryAt: entry, ExitAt: exit, EntryPrice: entryPrice, ExitPrice: exitPrice, Reason: reason, GrossBPS: gross(entryPrice, exitPrice, d).FloatString(6)}
}
func aggregate(days []Result, cost int64, base bool, d r001.Direction) Cost {
	c := Cost{RoundTripBPS: cost}
	values := []*big.Rat{}
	sum := new(big.Rat)
	for _, day := range days {
		// Comparing on the same evaluable days avoids a missing-data advantage.
		if day.Status == "unevaluable" {
			continue
		}
		t := day.Trade
		if base {
			t = day.Baseline
		}
		if t == nil {
			continue
		}
		v := new(big.Rat).Sub(gross(t.EntryPrice, t.ExitPrice, d), big.NewRat(cost, 1))
		sum.Add(sum, v)
		values = append(values, v)
		if v.Sign() > 0 {
			c.Winners++
		}
	}
	c.Trades = len(values)
	if c.Trades == 0 {
		return c
	}
	mean := new(big.Rat).Quo(sum, big.NewRat(int64(c.Trades), 1)).FloatString(6)
	c.MeanBPS = &mean
	sort.Slice(values, func(i, j int) bool { return values[i].Cmp(values[j]) < 0 })
	median := new(big.Rat).Set(values[c.Trades/2])
	if c.Trades%2 == 0 {
		median.Add(median, values[c.Trades/2-1])
		median.Quo(median, big.NewRat(2, 1))
	}
	m := median.FloatString(6)
	c.MedianBPS = &m
	return c
}
