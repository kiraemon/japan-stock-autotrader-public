package marketdata

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"
)

// JQuantsDayMatch is a historical same-provider reconciliation. It neither
// requires a later intraday window nor rejects prior corporate-action days:
// same-day open/close ratios use one raw basis. Current actions remain subject
// to the separate fixed-window guard. No external eligibility flag is accepted.
type JQuantsDayMatch struct {
	Version       string   `json:"version"`
	Date          string   `json:"date_jst"`
	Source        Source   `json:"minute_source"`
	DailyMatch    bool     `json:"same_provider_daily_ohlcv_match"`
	TurnoverMatch bool     `json:"same_provider_daily_turnover_match"`
	Reasons       []string `json:"reasons"`
	Quality       bool     `json:"quality_promoted"`
}

func CheckJQuantsTurnoverDay(ctx context.Context, d Dataset, date string, daily DailyBar) (JQuantsDayMatch, error) {
	r := JQuantsDayMatch{Version: "jquants-historical-day-turnover-match-v0.1", Date: date, Source: d.Source, Reasons: []string{}}
	if Validate(d) != nil || !IsJQuantsTurnover(d.Source.Format) || d.Origin != "observed" || d.Environment != "production" || !strings.HasSuffix(d.Symbol, "@1") {
		return r, errors.New("履歴日照合の売買代金付き分足を確認してください")
	}
	at, e := time.Parse("2006-01-02", date)
	if e != nil || at.Format("2006-01-02") != date {
		return r, errors.New("履歴日照合の日付を確認してください")
	}
	if ctx.Err() != nil {
		return r, errors.New("履歴日の照合を中断しました")
	}
	var day *Day
	for i := range d.Days {
		if d.Days[i].Date == date {
			day = &d.Days[i]
			break
		}
	}
	if day == nil {
		r.Reasons = append(r.Reasons, "historical_minute_day_missing")
		return r, nil
	}
	for _, issue := range day.Issues {
		switch issue {
		case "coverage_unverified", "trading_status_unverified", "source_crosscheck_unverified", "corporate_actions_unverified":
		default:
			r.Reasons = append(r.Reasons, "historical_day_quality_unverified")
		}
	}
	sum := new(big.Rat)
	for _, b := range day.Bars {
		if len(b.Issues) > 0 || b.Start.Add(time.Minute).After(d.Source.ReceivedAt) {
			r.Reasons = append(r.Reasons, "historical_minute_quality_or_close_unverified")
		}
		v, _ := new(big.Rat).SetString(b.TurnoverJPY)
		sum.Add(sum, v)
	}
	for _, event := range day.Events {
		if event.Time.After(d.Source.ReceivedAt) {
			r.Reasons = append(r.Reasons, "historical_boundary_unclosed")
		}
		v, _ := new(big.Rat).SetString(event.TurnoverJPY)
		sum.Add(sum, v)
	}
	if daily.Date != date || daily.Code != strings.TrimSuffix(d.Symbol, "@1")+"0" || !daily.HasTrades || daily.Volume < 0 || !ValidPrices(Bar{Open: daily.Open, High: daily.High, Low: daily.Low, Close: daily.Close}) {
		r.Reasons = append(r.Reasons, "historical_daily_reference_unavailable")
		return r, nil
	}
	r.DailyMatch, e = jquantsDailyMatch(ctx, *day, daily)
	if e != nil {
		return r, e
	}
	if !r.DailyMatch {
		r.Reasons = append(r.Reasons, "historical_daily_ohlcv_mismatch")
	}
	if len(daily.Turnover) <= 128 && turnoverExactPattern.MatchString(daily.Turnover) {
		value, ok := new(big.Rat).SetString(daily.Turnover)
		r.TurnoverMatch = ok && value.Sign() > 0 && sum.Cmp(value) == 0
	}
	if !r.TurnoverMatch {
		r.Reasons = append(r.Reasons, "historical_daily_turnover_mismatch")
	}
	return r, nil
}
