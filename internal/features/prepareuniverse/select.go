package prepareuniverse

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

const Version = "s01-universe-v0.1"
const Lookback = 20
const Size = 30

var exactPattern = regexp.MustCompile(`^[1-9][0-9]*(/[1-9][0-9]*)?$`)

type Pick struct {
	Code string `json:"code"`
	Mean string `json:"mean_turnover_exact"`
}
type Exclusion struct {
	Code  string   `json:"code"`
	Dates []string `json:"unavailable_dates"`
}
type Day struct {
	Date     string      `json:"date_jst"`
	Previous []string    `json:"previous_sessions"`
	Eligible int         `json:"eligible_master_count"`
	Missing  []Exclusion `json:"excluded_missing_liquidity"`
	Picks    []Pick      `json:"selected"`
	Problems []string    `json:"problems"`
}
type Report struct {
	Version         string              `json:"version"`
	Days            []Day               `json:"days"`
	Union           []string            `json:"union_codes"`
	MonthlyCodes    map[string][]string `json:"monthly_codes_with_observed_turnover"`
	Limitations     []string            `json:"limitations"`
	QualityPromoted bool                `json:"quality_promoted"`
	TradingEligible bool                `json:"trading_eligible"`
	StrategyAdopted bool                `json:"strategy_adopted"`
	NewPNL          bool                `json:"new_pnl_evaluation"`
}

// Select consumes a complete explicit calendar; it never infers business dates
// from observed data. Missing input stays in the reference window and report.
func Select(ctx context.Context, daily []marketdata.DailyBar, listings []Listing, sessions []string, from, to string) (Report, error) {
	r := Report{Version: Version, Days: []Day{}, Union: []string{}, MonthlyCodes: map[string][]string{}, Limitations: []string{"historical_publication_and_revision_times_unverified", "unadjusted_daily_turnover_not_price_or_capital_filter", "liquidity_universe_only_no_intraday_signal_or_pnl", "no_strategy_adoption_or_order_permission"}}
	if !validDate(from) || !validDate(to) || from > to || len(sessions) > 3660 || len(sessions) == 0 || len(daily) > 2000000 || len(listings) > 2000000 {
		return r, errors.New("母集団の期間/入力件数を確認してください")
	}
	allowedDates := map[string]bool{}
	for i, date := range sessions {
		at, err := time.Parse("2006-01-02", date)
		if err != nil || !validDate(date) || at.Weekday() == time.Saturday || at.Weekday() == time.Sunday || (i > 0 && sessions[i-1] >= date) {
			return r, errors.New("母集団の営業日カレンダーを確認してください")
		}
		allowedDates[date] = true
	}
	if !allowedDates[from] || !allowedDates[to] {
		return r, errors.New("評価期間の両端が公式営業日にありません")
	}
	amounts := map[string]map[string]*big.Rat{}
	for _, d := range daily {
		if ctx.Err() != nil {
			return r, errors.New("母集団計算を中断しました")
		}
		if !allowedDates[d.Date] || !codePattern.MatchString(d.Code) {
			return r, errors.New("日足の日付/コードを確認してください")
		}
		if amounts[d.Date] == nil {
			amounts[d.Date] = map[string]*big.Rat{}
		}
		if _, exists := amounts[d.Date][d.Code]; exists {
			return r, errors.New("日足の日付/コードが重複しています")
		}
		var amount *big.Rat
		if d.HasTrades {
			if len(d.Turnover) == 0 || len(d.Turnover) > 256 || !exactPattern.MatchString(d.Turnover) {
				return r, errors.New("日足の売買代金を確認してください")
			}
			var ok bool
			amount, ok = new(big.Rat).SetString(d.Turnover)
			if !ok || amount.Sign() <= 0 {
				return r, errors.New("日足の売買代金を確認してください")
			}
		} else if d.Turnover != "" {
			return r, errors.New("無約定の日足に売買代金が指定されています")
		}
		amounts[d.Date][d.Code] = amount
	}
	masters := map[string]map[string]Listing{}
	for _, m := range listings {
		if ctx.Err() != nil {
			return r, errors.New("母集団計算を中断しました")
		}
		if !allowedDates[m.Date] || !codePattern.MatchString(m.Code) {
			return r, errors.New("銘柄一覧の日付/コードを確認してください")
		}
		if masters[m.Date] == nil {
			masters[m.Date] = map[string]Listing{}
		}
		if _, exists := masters[m.Date][m.Code]; exists {
			return r, errors.New("銘柄一覧の日付/コードが重複しています")
		}
		masters[m.Date][m.Code] = m
	}
	union := map[string]bool{}
	for i, date := range sessions {
		if date < from || date > to {
			continue
		}
		if ctx.Err() != nil {
			return r, errors.New("母集団計算を中断しました")
		}
		d := Day{Date: date, Previous: []string{}, Missing: []Exclusion{}, Picks: []Pick{}, Problems: []string{}}
		if i < Lookback {
			d.Problems = append(d.Problems, "calendar_warmup_short")
			r.Days = append(r.Days, d)
			continue
		}
		d.Previous = append(d.Previous, sessions[i-Lookback:i]...)
		if len(masters[date]) == 0 {
			d.Problems = append(d.Problems, "historical_master_missing")
			r.Days = append(r.Days, d)
			continue
		}
		type candidate struct {
			Pick
			value *big.Rat
		}
		candidates := []candidate{}
		codes := []string{}
		for code, m := range masters[date] {
			if m.Product == "011" && strings.HasSuffix(code, "0") && (m.Market == "0111" || m.Market == "0112" || m.Market == "0113") {
				codes = append(codes, code)
			}
		}
		sort.Strings(codes)
		d.Eligible = len(codes)
		for _, code := range codes {
			sum := new(big.Rat)
			missing := []string{}
			for _, prior := range d.Previous {
				amount := amounts[prior][code]
				if amount == nil {
					missing = append(missing, prior)
				} else {
					sum.Add(sum, amount)
				}
			}
			if len(missing) > 0 {
				d.Missing = append(d.Missing, Exclusion{code, missing})
				continue
			}
			mean := new(big.Rat).Quo(sum, big.NewRat(Lookback, 1))
			candidates = append(candidates, candidate{Pick{code, mean.RatString()}, mean})
		}
		sort.Slice(candidates, func(i, j int) bool {
			cmp := candidates[i].value.Cmp(candidates[j].value)
			if cmp == 0 {
				return candidates[i].Code < candidates[j].Code
			}
			return cmp > 0
		})
		count := Size
		if len(candidates) < count {
			count = len(candidates)
			d.Problems = append(d.Problems, "fewer_than_30_complete_candidates")
		}
		for _, c := range candidates[:count] {
			d.Picks = append(d.Picks, c.Pick)
			union[c.Code] = true
		}
		r.Days = append(r.Days, d)
	}
	for code := range union {
		r.Union = append(r.Union, code)
	}
	sort.Strings(r.Union)
	for _, date := range sessions {
		month := date[:7]
		if _, ok := r.MonthlyCodes[month]; !ok {
			r.MonthlyCodes[month] = []string{}
		}
		for _, code := range r.Union {
			if amounts[date][code] == nil {
				continue
			}
			found := false
			for _, old := range r.MonthlyCodes[month] {
				if old == code {
					found = true
					break
				}
			}
			if !found {
				r.MonthlyCodes[month] = append(r.MonthlyCodes[month], code)
			}
		}
	}
	for month := range r.MonthlyCodes {
		sort.Strings(r.MonthlyCodes[month])
	}
	return r, nil
}
