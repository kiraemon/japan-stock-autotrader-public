// Package backtestintraday diagnoses two fixed morning price hypotheses. It
// has no I/O, execution interface, capital book or generic quality override.
package backtestintraday

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/round3"
)

const Version = "fixed-morning-next-open-no-price-stop-v0.1"

type Trade struct {
	Direction  string    `json:"direction"`
	SignalAt   time.Time `json:"signal_at"`
	EntryAt    time.Time `json:"entry_at"`
	ExitAt     time.Time `json:"exit_at"`
	EntryPrice int64     `json:"entry_price_scaled"`
	ExitPrice  int64     `json:"exit_price_scaled"`
	GrossBPS   string    `json:"gross_return_bps_exact"`
	Reason     string    `json:"exit_reason"`
	Precision  string    `json:"exit_time_precision"`
}
type Report struct {
	Version string                       `json:"version"`
	Rule    string                       `json:"rule"`
	Date    string                       `json:"date_jst"`
	Symbol  string                       `json:"symbol"`
	Source  marketdata.Source            `json:"minute_source"`
	Status  string                       `json:"status"`
	Reasons []string                     `json:"reasons"`
	Window  marketdata.JQuantsWindow     `json:"price_window"`
	History []marketdata.JQuantsDayMatch `json:"historical_day_checks"`
	Noise   *round3.NoiseResult          `json:"noise,omitempty"`
	Gap     *round3.GapResult            `json:"gap,omitempty"`
	Trade   *Trade                       `json:"trade,omitempty"`
	Quality bool                         `json:"quality_promoted"`
	Trading bool                         `json:"trading_eligible"`
}

// Diagnose independently recalculates source/day/window/feature checks. The
// caller binds previous to its pinned official calendar, not observed dates.
func Diagnose(ctx context.Context, d marketdata.Dataset, date string, previous []string, refs []marketdata.DailyBar, rule string) (Report, error) {
	r := Report{Version: Version, Rule: rule, Date: date, Symbol: d.Symbol, Source: d.Source, Status: "unevaluable", Reasons: []string{}, History: []marketdata.JQuantsDayMatch{}}
	if rule != round3.NoiseRule && rule != round3.GapRule || len(previous) != 14 || len(refs) > 16 || len(d.Days) > 15 || marketdata.Validate(d) != nil || !marketdata.IsJQuantsTurnover(d.Source.Format) || d.Origin != "observed" || d.Environment != "production" {
		return r, errors.New("固定朝戦略の参照日/売買代金付き分足/版を確認してください")
	}
	if ctx.Err() != nil {
		return r, errors.New("朝戦略の価格診断を中断しました")
	}
	wanted := map[string]bool{date: true}
	last := ""
	for _, prior := range previous {
		at, e := time.Parse("2006-01-02", prior)
		if e != nil || at.Format("2006-01-02") != prior || prior <= last || prior >= date || at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
			return r, errors.New("固定朝戦略の過去14参照日を確認してください")
		}
		wanted[prior] = true
		last = prior
	}
	daily := map[string]marketdata.DailyBar{}
	for _, ref := range refs {
		if ref.Code != strings.TrimSuffix(d.Symbol, "@1")+"0" || !wanted[ref.Date] {
			continue
		}
		if _, exists := daily[ref.Date]; exists {
			return r, errors.New("朝戦略の日足参照が重複しています")
		}
		daily[ref.Date] = ref
	}
	days := map[string]marketdata.Day{}
	for _, day := range d.Days {
		if !wanted[day.Date] {
			return r, errors.New("朝戦略の対象外の分足日を確認してください")
		}
		days[day.Date] = day
	}
	view := func(day string) marketdata.Dataset {
		v := d
		if found, exists := days[day]; exists {
			v.Days = []marketdata.Day{found}
		}
		return v
	}
	window, e := marketdata.CheckJQuantsTurnoverWindow(ctx, view(date), date, daily[date])
	if e != nil {
		return r, e
	}
	r.Window = window
	if !window.Eligible {
		r.Reasons = append(r.Reasons, window.Reasons...)
		return r, nil
	}
	prior, ok := daily[previous[13]]
	if !ok || !prior.HasTrades || !marketdata.ValidPrices(marketdata.Bar{Open: prior.Open, High: prior.High, Low: prior.Low, Close: prior.Close}) {
		r.Reasons = append(r.Reasons, "previous_official_daily_close_unavailable")
		return r, nil
	}
	var decision round3.Decision
	if rule == round3.NoiseRule {
		history := []marketdata.Day{}
		for _, date := range previous {
			check, e := marketdata.CheckJQuantsTurnoverDay(ctx, view(date), date, daily[date])
			if e != nil {
				return r, e
			}
			r.History = append(r.History, check)
			r.Reasons = append(r.Reasons, check.Reasons...)
			if day, exists := days[date]; exists {
				history = append(history, day)
			}
		}
		if len(r.Reasons) > 0 {
			return r, nil
		}
		result, e := round3.NoiseArea(ctx, date, previous, history, days[date], prior.Close)
		if e != nil {
			return r, e
		}
		r.Noise = &result
		decision = result.Decision
	} else {
		result, e := round3.GapReversal(ctx, date, days[date], prior.Close)
		if e != nil {
			return r, e
		}
		r.Gap = &result
		decision = result.Decision
	}
	r.Status = decision.Status
	if decision.Status != "candidate" {
		if decision.Reason != "" {
			r.Reasons = append(r.Reasons, decision.Reason)
		}
		return r, nil
	}
	byTime := map[time.Time]marketdata.Bar{}
	for _, b := range days[date].Bars {
		byTime[b.Start.UTC()] = b
	}
	entry, entryOK := byTime[decision.EntryAt.UTC()]
	exit, exitOK := byTime[decision.ExitAt.UTC()]
	if !entryOK || !exitOK || len(entry.Issues) > 0 || len(exit.Issues) > 0 {
		r.Status = "unevaluable"
		r.Reasons = append(r.Reasons, "entry_or_time_exit_minute_unavailable")
		return r, nil
	}
	move := exit.Open - entry.Open
	if decision.Direction == "short" {
		move = -move
	}
	r.Trade = &Trade{Direction: decision.Direction, SignalAt: decision.SignalAt, EntryAt: decision.EntryAt, ExitAt: decision.ExitAt, EntryPrice: entry.Open, ExitPrice: exit.Open, GrossBPS: big.NewRat(move*10000, entry.Open).RatString(), Reason: "fixed_morning_time_exit", Precision: "minute_open_model_not_actual_fill"}
	r.Status = "traded"
	return r, nil
}
