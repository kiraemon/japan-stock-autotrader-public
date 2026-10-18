package s01

import (
	"context"
	"errors"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

type Direction string

const Long Direction = "long"
const Short Direction = "short"

type Range struct {
	Open  int64 `json:"open"`
	High  int64 `json:"high"`
	Low   int64 `json:"low"`
	Close int64 `json:"close"`
}
type EntryDecision struct {
	Status      string     `json:"status"`
	Direction   Direction  `json:"direction,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	CandidateAt *time.Time `json:"candidate_bar_start,omitempty"`
	SignalAt    *time.Time `json:"signal_at,omitempty"`
	EntryAt     *time.Time `json:"entry_at,omitempty"`
}

// FirstBreakout is a price-pattern calculation, not an order instruction.
// The caller supplies the completed 09:00-09:04 range and gates RVOL/ATR.
// The first missing minute before the candidate prevents a later invented entry.
func FirstBreakout(ctx context.Context, date string, opening Range, bars []marketdata.Bar) (EntryDecision, error) {
	r := EntryDecision{Status: "no_trade"}
	d, err := time.Parse("2006-01-02", date)
	if err != nil || d.Format("2006-01-02") != date || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday || !marketdata.ValidPrices(marketdata.Bar{Open: opening.Open, High: opening.High, Low: opening.Low, Close: opening.Close}) {
		return r, errors.New("S01の評価日と確定した寄り5分レンジを確認してください")
	}
	if ctx.Err() != nil {
		return r, errors.New("S01の参入判定を中断しました")
	}
	if opening.Close == opening.Open {
		r.Reason = "opening_doji"
		return r, nil
	}
	r.Direction = Long
	if opening.Close < opening.Open {
		r.Direction = Short
	}
	byTime := map[time.Time]marketdata.Bar{}
	for _, b := range bars {
		if marketdata.Date(b.Start) != date || b.Start.Before(marketdata.Clock(date, 9, 5)) || b.Start.After(marketdata.Clock(date, 10, 29)) {
			continue
		}
		key := b.Start.UTC()
		if _, exists := byTime[key]; exists {
			return r, errors.New("S01の参入窓の分足が重複しています")
		}
		byTime[key] = b
	}
	for at := marketdata.Clock(date, 9, 5); !at.After(marketdata.Clock(date, 10, 29)); at = at.Add(time.Minute) {
		if ctx.Err() != nil {
			return r, errors.New("S01の参入判定を中断しました")
		}
		b, exists := byTime[at.UTC()]
		if !exists || !marketdata.ValidPrices(b) || len(b.Issues) > 0 {
			r.Status = "unevaluable"
			r.Reason = "missing_or_invalid_signal_minute"
			return r, nil
		}
		if (r.Direction == Long && b.Close > opening.High) || (r.Direction == Short && b.Close < opening.Low) {
			start := at
			known := at.Add(time.Minute)
			entry := known
			r.Status = "candidate"
			r.CandidateAt = &start
			r.SignalAt = &known
			r.EntryAt = &entry
			return r, nil
		}
	}
	r.Reason = "no_directional_close_breakout"
	return r, nil
}
