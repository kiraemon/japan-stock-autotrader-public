// Package backtests01 contains the fixed S01-JP20 price replay. It has no
// broker interface. Normal Run rejects observed data whose quality is unverified.
package backtests01

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

const ModelVersion = "s01-next-open-stop-or-20m-v0.1"

type Trade struct {
	Direction         s01.Direction `json:"direction"`
	SignalAt          time.Time     `json:"signal_at"`
	EntryAt           time.Time     `json:"entry_at"`
	ExitAt            time.Time     `json:"exit_at"`
	ExitBarStart      time.Time     `json:"exit_bar_start"`
	ExitTimePrecision string        `json:"exit_time_precision"`
	EntryPrice        int64         `json:"entry_price"`
	ExitPrice         int64         `json:"exit_price"`
	StopPrice         int64         `json:"stop_price"`
	Reason            string        `json:"exit_reason"`
	GrossBPS          string        `json:"gross_return_bps"`
}
type Report struct {
	Rule            string             `json:"strategy_version"`
	Model           string             `json:"execution_model_version"`
	Date            string             `json:"date_jst"`
	Symbol          string             `json:"symbol"`
	SourceSHA       string             `json:"source_sha256"`
	Scope           string             `json:"scope"`
	Status          string             `json:"status"`
	Reasons         []string           `json:"reasons"`
	Decision        *s01.EntryDecision `json:"decision,omitempty"`
	Trade           *Trade             `json:"trade,omitempty"`
	QualityPromoted bool               `json:"quality_promoted"`
	TradingEligible bool               `json:"trading_eligible"`
	StrategyAdopted bool               `json:"strategy_adopted"`
}

// Run is deliberately synthetic-only. A later bounded provider diagnostic must
// retain original input evidence separately; this is not a quality override.
func Run(ctx context.Context, d marketdata.Dataset, date string, opening s01.Range, stopDistance int64) (Report, error) {
	r := Report{Rule: s01.Version, Model: ModelVersion, Date: date, Symbol: d.Symbol, SourceSHA: d.Source.SHA256, Scope: "synthetic_logic_test", Status: "unevaluable", Reasons: []string{}}
	if err := marketdata.Validate(d); err != nil {
		return r, err
	}
	if ctx.Err() != nil {
		return r, errors.New("S01再生を中断しました")
	}
	if stopDistance <= 0 || stopDistance > marketdata.MaxPrice {
		return r, errors.New("S01の正のATR損切り距離を確認してください")
	}
	if d.Origin != "synthetic" {
		r.Scope = "observed_quality_unverified"
		r.Reasons = append(r.Reasons, "observed_price_diagnostic_required")
		return r, nil
	}
	var day *marketdata.Day
	for i := range d.Days {
		if d.Days[i].Date == date {
			day = &d.Days[i]
			break
		}
	}
	if day == nil {
		r.Reasons = append(r.Reasons, "day_missing")
		return r, nil
	}
	if len(day.Issues) > 0 {
		r.Reasons = append(r.Reasons, day.Issues...)
		return r, nil
	}
	decision, err := s01.FirstBreakout(ctx, date, opening, day.Bars)
	if err != nil {
		return r, err
	}
	r.Decision = &decision
	r.Status = decision.Status
	if decision.Status != "candidate" {
		if decision.Reason != "" {
			r.Reasons = append(r.Reasons, decision.Reason)
		}
		return r, nil
	}
	trade, reason, err := execute(ctx, day.Bars, decision, stopDistance)
	if err != nil {
		return r, err
	}
	if reason != "" {
		r.Status = "unevaluable"
		r.Reasons = append(r.Reasons, reason)
		return r, nil
	}
	r.Status = "traded"
	r.Trade = trade
	return r, nil
}

func execute(ctx context.Context, bars []marketdata.Bar, decision s01.EntryDecision, distance int64) (*Trade, string, error) {
	byTime := map[time.Time]marketdata.Bar{}
	for _, b := range bars {
		byTime[b.Start.UTC()] = b
	}
	entry, ok := byTime[decision.EntryAt.UTC()]
	if !ok || len(entry.Issues) > 0 {
		return nil, "entry_minute_unavailable", nil
	}
	stop := entry.Open - distance
	if decision.Direction == s01.Short {
		stop = entry.Open + distance
	}
	if stop <= 0 || stop > marketdata.MaxPrice {
		return nil, "stop_price_out_of_range", nil
	}
	t := &Trade{Direction: decision.Direction, SignalAt: *decision.SignalAt, EntryAt: *decision.EntryAt, EntryPrice: entry.Open, StopPrice: stop}
	for minute := 0; minute <= 20; minute++ {
		if ctx.Err() != nil {
			return nil, "", errors.New("S01仮想退出を中断しました")
		}
		at := decision.EntryAt.Add(time.Duration(minute) * time.Minute)
		b, exists := byTime[at.UTC()]
		if !exists || len(b.Issues) > 0 {
			return nil, "holding_or_deadline_minute_unavailable", nil
		}
		t.ExitAt = at
		t.ExitBarStart = at
		t.ExitTimePrecision = "minute_open_model"
		if minute == 20 {
			t.ExitPrice = b.Open
			t.Reason = "maximum_hold_20m"
			break
		}
		if (decision.Direction == s01.Long && b.Open <= stop) || (decision.Direction == s01.Short && b.Open >= stop) {
			t.ExitPrice = b.Open
			t.Reason = "adverse_gap_through_stop"
			break
		}
		if (decision.Direction == s01.Long && b.Low <= stop) || (decision.Direction == s01.Short && b.High >= stop) {
			t.ExitPrice = stop
			t.Reason = "intrabar_stop_touch"
			t.ExitAt = at.Add(time.Minute)
			t.ExitTimePrecision = "intrabar_unknown_capital_released_at_bar_end"
			break
		}
	}
	delta := t.ExitPrice - t.EntryPrice
	if t.Direction == s01.Short {
		delta = -delta
	}
	t.GrossBPS = new(big.Rat).Mul(big.NewRat(delta, t.EntryPrice), big.NewRat(10000, 1)).RatString()
	return t, "", nil
}
