// Package round3 implements two fixed price hypotheses, without I/O, execution,
// capital rules or a data-quality override. Callers own source/calendar binding
// and the ex-post diagnostic separately from these causal price calculations.
package round3

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

const NoiseRule = "NA30-JP-v0.1"
const GapRule = "GAP29-JP-v0.1"

type Decision struct {
	Rule      string    `json:"rule"`
	Status    string    `json:"status"`
	Direction string    `json:"direction,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	SignalAt  time.Time `json:"signal_at"`
	EntryAt   time.Time `json:"entry_at"`
	ExitAt    time.Time `json:"exit_at"`
}

type NoiseResult struct {
	Decision Decision `json:"decision"`
	Previous []string `json:"previous_sessions"`
	Sigma    string   `json:"sigma_exact,omitempty"`
	VWAP     string   `json:"vwap_jpy_exact,omitempty"`
	Upper    string   `json:"upper_jpy_exact,omitempty"`
	Lower    string   `json:"lower_jpy_exact,omitempty"`
}

type GapResult struct {
	Decision Decision `json:"decision"`
	Gap      string   `json:"gap_exact,omitempty"`
}

func dateOK(date string) bool {
	t, e := time.Parse("2006-01-02", date)
	return e == nil && t.Format("2006-01-02") == date && date >= "2024-11-05" && t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

func initial(rule, date string, minute, exitHour, exitMinute int) Decision {
	at := marketdata.Clock(date, 9, minute)
	return Decision{Rule: rule, Status: "unevaluable", SignalAt: at, EntryAt: at, ExitAt: marketdata.Clock(date, exitHour, exitMinute)}
}

func dayIssuesOK(day marketdata.Day) bool {
	for _, issue := range day.Issues {
		switch issue {
		case "coverage_unverified", "trading_status_unverified", "source_crosscheck_unverified", "corporate_actions_unverified":
		default:
			return false
		}
	}
	return true
}

// morning examines only completed bars up through 09:29. Missing intermediate
// history minutes are not filled; first actual open and actual 09:29 are needed.
// The caller's full fixed-window gate remains a separate stricter current check.
func morning(ctx context.Context, day marketdata.Day, amount bool) (int64, int64, *big.Rat, int64, string, error) {
	if len(day.Bars) > 325 {
		return 0, 0, nil, 0, "", errors.New("寄り入力の分足件数を確認してください")
	}
	if !dayIssuesOK(day) {
		return 0, 0, nil, 0, "unsupported_day_quality_issue", nil
	}
	start, end := marketdata.Clock(day.Date, 9, 0), marketdata.Clock(day.Date, 9, 29)
	first := time.Time{}
	open, close := int64(0), int64(0)
	sum := new(big.Rat)
	volume := new(big.Int)
	seen := map[time.Time]bool{}
	for _, b := range day.Bars {
		if ctx.Err() != nil {
			return 0, 0, nil, 0, "", errors.New("固定仮説の計算を中断しました")
		}
		if b.Start.Before(start) || b.Start.After(end) {
			continue
		}
		key := b.Start.UTC()
		if marketdata.Date(b.Start) != day.Date || !b.Start.Equal(b.Start.Truncate(time.Minute)) || seen[key] {
			return 0, 0, nil, 0, "", errors.New("寄り入力の日時/分足重複を確認してください")
		}
		seen[key] = true
		if !marketdata.ValidPrices(b) || len(b.Issues) > 0 {
			return 0, 0, nil, 0, "invalid_or_unclosed_morning_bar", nil
		}
		if first.IsZero() || b.Start.Before(first) {
			first = b.Start
			open = b.Open
		}
		if b.Start.Equal(end) {
			close = b.Close
		}
		if amount {
			if b.Volume == nil || *b.Volume <= 0 || !marketdata.ValidTurnover(b.TurnoverJPY, b.Volume, b.Low, b.High) {
				return 0, 0, nil, 0, "morning_turnover_or_volume_unavailable", nil
			}
			v, _ := new(big.Rat).SetString(b.TurnoverJPY)
			sum.Add(sum, v)
			volume.Add(volume, big.NewInt(*b.Volume))
		}
	}
	if first.IsZero() || first.After(start.Add(4*time.Minute)) {
		return 0, 0, nil, 0, "first_actual_open_later_than_0904_or_missing", nil
	}
	if close == 0 {
		return 0, 0, nil, 0, "actual_0929_close_missing", nil
	}
	if amount && (!volume.IsInt64() || volume.Sign() <= 0) {
		return 0, 0, nil, 0, "morning_volume_out_of_bounds", nil
	}
	return open, close, sum, volume.Int64(), "", nil
}

// NoiseArea uses fourteen exact caller-bound official reference sessions. It
// does not infer business dates from whichever observations happen to exist.
// Source hashes, full-day reconciliation and fixed 111-minute current-window
// eligibility must be checked again by the research diagnostic before replay.
func NoiseArea(ctx context.Context, date string, previous []string, past []marketdata.Day, current marketdata.Day, previousClose int64) (NoiseResult, error) {
	r := NoiseResult{Decision: initial(NoiseRule, date, 30, 10, 0), Previous: append([]string{}, previous...)}
	if !dateOK(date) || current.Date != date || len(previous) != 14 || len(past) > 3660 || previousClose <= 0 || previousClose > marketdata.MaxPrice {
		return r, errors.New("Noise Areaの対象日/参照14日/前日終値を確認してください")
	}
	if ctx.Err() != nil {
		return r, errors.New("固定仮説の計算を中断しました")
	}
	wanted := map[string]bool{}
	for i, d := range previous {
		if !dateOK(d) || d >= date || i > 0 && d <= previous[i-1] {
			return r, errors.New("Noise Areaの過去参照日と順序を確認してください")
		}
		wanted[d] = true
	}
	days := map[string]marketdata.Day{}
	for _, day := range past {
		if wanted[day.Date] {
			if _, exists := days[day.Date]; exists {
				return r, errors.New("Noise Areaの過去日が重複しています")
			}
			days[day.Date] = day
		}
	}
	sigma := new(big.Rat)
	for _, date := range previous {
		day, ok := days[date]
		if !ok {
			r.Decision.Reason = "exact_historical_session_missing"
			return r, nil
		}
		open, close, _, _, reason, err := morning(ctx, day, false)
		if err != nil {
			return r, err
		}
		if reason != "" {
			r.Decision.Reason = "historical_" + reason
			return r, nil
		}
		move := new(big.Rat).Sub(big.NewRat(close, open), big.NewRat(1, 1))
		move.Abs(move)
		sigma.Add(sigma, move)
	}
	sigma.Quo(sigma, big.NewRat(14, 1))
	open, close, amount, volume, reason, err := morning(ctx, current, true)
	if err != nil {
		return r, err
	}
	if reason != "" {
		r.Decision.Reason = reason
		return r, nil
	}
	upperBase, lowerBase := open, previousClose
	if previousClose > open {
		upperBase = previousClose
		lowerBase = open
	}
	upper := new(big.Rat).Mul(big.NewRat(upperBase, marketdata.PriceScale), new(big.Rat).Add(big.NewRat(1, 1), sigma))
	lower := new(big.Rat).Mul(big.NewRat(lowerBase, marketdata.PriceScale), new(big.Rat).Sub(big.NewRat(1, 1), sigma))
	vwap := new(big.Rat).Quo(amount, big.NewRat(volume, 1))
	last := big.NewRat(close, marketdata.PriceScale)
	r.Sigma, r.VWAP, r.Upper, r.Lower = sigma.RatString(), vwap.RatString(), upper.RatString(), lower.RatString()
	r.Decision.Status = "no_trade"
	r.Decision.Reason = "no_strict_noise_and_vwap_break"
	if last.Cmp(upper) > 0 && last.Cmp(vwap) > 0 {
		r.Decision.Status = "candidate"
		r.Decision.Direction = "long"
		r.Decision.Reason = "above_noise_and_vwap"
	}
	if last.Cmp(lower) < 0 && last.Cmp(vwap) < 0 {
		r.Decision.Status = "candidate"
		r.Decision.Direction = "short"
		r.Decision.Reason = "below_noise_and_vwap"
	}
	return r, nil
}

// GapReversal knows the opening price only after the actual 09:00 minute has
// completed. That price is a signal input, never the assumed execution price.
func GapReversal(ctx context.Context, date string, current marketdata.Day, previousClose int64) (GapResult, error) {
	r := GapResult{Decision: initial(GapRule, date, 1, 9, 30)}
	if !dateOK(date) || current.Date != date || len(current.Bars) > 325 || previousClose <= 0 || previousClose > marketdata.MaxPrice {
		return r, errors.New("Gap Reversalの対象日/前日終値を確認してください")
	}
	if ctx.Err() != nil {
		return r, errors.New("固定仮説の計算を中断しました")
	}
	if !dayIssuesOK(current) {
		r.Decision.Reason = "unsupported_day_quality_issue"
		return r, nil
	}
	var opening *marketdata.Bar
	for i := range current.Bars {
		if ctx.Err() != nil {
			return r, errors.New("固定仮説の計算を中断しました")
		}
		b := &current.Bars[i]
		if !b.Start.Equal(marketdata.Clock(date, 9, 0)) {
			continue
		}
		if opening != nil {
			return r, errors.New("Gap Reversalの09:00足が重複しています")
		}
		opening = b
	}
	if opening == nil {
		r.Decision.Reason = "actual_0900_minute_missing"
		return r, nil
	}
	if !marketdata.ValidPrices(*opening) || len(opening.Issues) > 0 {
		r.Decision.Reason = "actual_0900_minute_invalid_or_unclosed"
		return r, nil
	}
	gap := new(big.Rat).Sub(big.NewRat(opening.Open, previousClose), big.NewRat(1, 1))
	r.Gap = gap.RatString()
	r.Decision.Status = "no_trade"
	r.Decision.Reason = "gap_not_strictly_over_two_percent"
	if gap.Cmp(big.NewRat(1, 50)) > 0 {
		r.Decision.Status = "candidate"
		r.Decision.Direction = "short"
		r.Decision.Reason = "fade_positive_gap"
	}
	if gap.Cmp(big.NewRat(-1, 50)) < 0 {
		r.Decision.Status = "candidate"
		r.Decision.Direction = "long"
		r.Decision.Reason = "fade_negative_gap"
	}
	return r, nil
}
