// Package researchbook accounts for fixed hypothetical signals. It has no
// broker interface, live risk permission, or claim about actual fills/inventory.
package researchbook

import (
	"context"
	"errors"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"math/big"
	"regexp"
	"sort"
	"time"
)

const Version = "ordinary-margin-research-book-v0.1"

type Trade struct {
	Direction  string    `json:"direction"`
	EntryAt    time.Time `json:"entry_at"`
	ExitAt     time.Time `json:"exit_at"`
	EntryPrice int64     `json:"entry_price_scaled"`
	ExitPrice  int64     `json:"exit_price_scaled"`
	Reason     string    `json:"exit_reason"`
}
type Case struct {
	Date    string   `json:"date_jst"`
	Symbol  string   `json:"symbol"`
	Rank    int      `json:"liquidity_rank"`
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Common  bool     `json:"common_evaluable_case"`
	Trade   *Trade   `json:"trade,omitempty"`
}
type Scenario struct {
	FrictionBPS int64  `json:"round_trip_friction_bps"`
	Commission  string `json:"commission_scenario"`
	ShortExtra  int64  `json:"short_extra_jpy"`
	CommonOnly  bool   `json:"common_cases_only"`
}
type Costs struct {
	Gross       string `json:"gross_jpy_exact"`
	Commission  string `json:"commission_jpy_exact"`
	Credit      string `json:"credit_jpy_exact"`
	Friction    string `json:"friction_jpy_exact"`
	ShortExtra  string `json:"short_extra_jpy_exact"`
	Net         string `json:"net_before_data_jpy_exact"`
	EntryBudget string `json:"entry_budget_jpy_ceil"`
}
type Entry struct {
	Date            string `json:"date_jst"`
	Symbol          string `json:"symbol"`
	Rank            int    `json:"liquidity_rank"`
	Status          string `json:"status"`
	Trade           Trade  `json:"trade"`
	Costs           Costs  `json:"costs"`
	EquityBefore    string `json:"realized_equity_before_jpy_exact"`
	EquityAfter     string `json:"realized_equity_after_jpy_exact,omitempty"`
	FitsIndependent bool   `json:"fits_independent_400000_jpy_budget"`
}
type Report struct {
	Version              string            `json:"version"`
	Scenario             Scenario          `json:"scenario"`
	Cases                int               `json:"cases"`
	Common               int               `json:"common_evaluable_cases"`
	NoTrade              int               `json:"no_trade_cases"`
	Unevaluable          int               `json:"unevaluable_cases"`
	ExcludedCommon       int               `json:"excluded_from_common_book"`
	Candidates           int               `json:"candidates"`
	Executed             int               `json:"executed"`
	Busy                 int               `json:"skipped_busy"`
	Capital              int               `json:"skipped_capital"`
	IndependentFit       int               `json:"independent_budget_fit_candidates"`
	Winners              int               `json:"winning_executed_trades"`
	StartEquity          string            `json:"starting_equity_jpy_exact"`
	EndEquity            string            `json:"ending_equity_before_data_jpy_exact"`
	Gross                string            `json:"gross_jpy_exact"`
	Commission           string            `json:"commission_jpy_exact"`
	Credit               string            `json:"credit_jpy_exact"`
	Friction             string            `json:"friction_jpy_exact"`
	ShortExtra           string            `json:"short_extra_jpy_exact"`
	Net                  string            `json:"net_before_data_jpy_exact"`
	DataFee              string            `json:"allocated_data_fee_jpy_exact"`
	AfterData            string            `json:"net_after_data_jpy_exact"`
	Drawdown             string            `json:"maximum_realized_equity_drawdown_jpy_exact"`
	SymbolNet            map[string]string `json:"executed_net_by_symbol_jpy_exact"`
	LargestPositiveShare string            `json:"largest_symbol_share_of_positive_net_exact"`
	LeaveBestOut         string            `json:"leave_best_executed_symbol_out_after_data_jpy_exact"`
	Decision             string            `json:"research_gate"`
	GateReasons          []string          `json:"gate_reasons"`
	Limitations          []string          `json:"limitations"`
	Ledger               []Entry           `json:"ledger"`
	TradingEligible      bool              `json:"trading_eligible"`
	StrategyAdopted      bool              `json:"strategy_adopted"`
	BorrowingVerified    bool              `json:"borrowing_inventory_verified"`
	ReplayClock          string            `json:"fixed_morning_price_clock,omitempty"`
}

var symbolPattern = regexp.MustCompile(`^[0-9A-Z]{4}@1$`)

func validDate(date string) bool {
	at, e := time.Parse("2006-01-02", date)
	return e == nil && at.Format("2006-01-02") == date && at.Weekday() != time.Saturday && at.Weekday() != time.Sunday
}
func integer(v int64) *big.Rat { return big.NewRat(v, 1) }
func exact(s string) *big.Rat  { v, _ := new(big.Rat).SetString(s); return v }
func ceil(r *big.Rat) *big.Rat {
	q, m := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), m)
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return new(big.Rat).SetInt(q)
}
func notional(price int64) *big.Rat {
	return new(big.Rat).Mul(integer(price), big.NewRat(100, marketdata.PriceScale))
}
func commission(n *big.Rat, mode string) *big.Rat {
	if mode == "sor_conditional_zero" {
		return integer(0)
	}
	for _, band := range [][2]int64{{100000, 99}, {200000, 148}, {500000, 198}} {
		if n.Cmp(integer(band[0])) <= 0 {
			return integer(band[1])
		}
	}
	return integer(385)
}
func quote(t Trade, s Scenario) Costs {
	entry, exit := notional(t.EntryPrice), notional(t.ExitPrice)
	gross := new(big.Rat).Sub(exit, entry)
	apr := int64(280)
	extra := integer(0)
	if t.Direction == "short" {
		gross.Neg(gross)
		apr = 110
		extra = integer(s.ShortExtra)
	}
	credit := ceil(new(big.Rat).Mul(entry, big.NewRat(apr, 10000*365)))
	friction := new(big.Rat).Mul(entry, big.NewRat(s.FrictionBPS, 10000))
	entryCommission := commission(entry, s.Commission)
	fees := new(big.Rat).Add(entryCommission, commission(exit, s.Commission))
	net := new(big.Rat).Sub(gross, fees)
	net.Sub(net, credit)
	net.Sub(net, friction)
	net.Sub(net, extra)
	margin := new(big.Rat).Mul(entry, big.NewRat(3, 10))
	if margin.Cmp(integer(300000)) < 0 {
		margin = integer(300000)
	}
	// Entry reserves use the entry price for both fee legs, never the future exit.
	reserve := new(big.Rat).Add(margin, new(big.Rat).Mul(entryCommission, integer(2)))
	reserve.Add(reserve, credit)
	reserve.Add(reserve, friction)
	reserve.Add(reserve, extra)
	return Costs{gross.RatString(), fees.RatString(), credit.RatString(), friction.RatString(), extra.RatString(), net.RatString(), ceil(reserve).RatString()}
}

// Run starts each supplied partition at 400,000 JPY. Data fees are allocated
// from the complete study calendar, reported separately from broker equity.
// Missing cases stay in coverage counts. CommonOnly changes only book inputs.
func Run(ctx context.Context, cases []Case, allSessions, partition []string, s Scenario) (Report, error) {
	return run(ctx, cases, allSessions, partition, s, "")
}

const Morning30 = "0930-1000-next-minute-open"
const Morning29 = "0901-0930-next-minute-open"
const Morning30VWAP = "0930-before1000-vwap-next-minute-open"

// RunTimedMorning changes only the fixed replay-clock contract. Accounting,
// capacity and cost/gates are shared with Run. It is not a generic live window.
func RunTimedMorning(ctx context.Context, cases []Case, allSessions, partition []string, s Scenario, clock string) (Report, error) {
	if clock != Morning30 && clock != Morning29 && clock != Morning30VWAP {
		return Report{}, errors.New("固定した朝の価格再生時刻を確認してください")
	}
	return run(ctx, cases, allSessions, partition, s, clock)
}

func run(ctx context.Context, cases []Case, allSessions, partition []string, s Scenario, clock string) (Report, error) {
	r := Report{Version: Version, Scenario: s, StartEquity: "400000", SymbolNet: map[string]string{}, GateReasons: []string{}, Ledger: []Entry{}, Limitations: []string{"research_assumptions_not_real_risk_limits", "quantity_100_lot_100_margin_30pct_minimum_300000_jpy", "partition_equity_reset_400000_jpy", "one_position_realized_equity_only_exits_before_same_timestamp_entries", "entry_fee_reserve_uses_entry_not_future_exit_notional", "one_day_365_basis_credit_ceil_yen_final_broker_rounding_unverified", "friction_entry_notional_once_per_round_trip", "data_fee_external_to_broker_equity", "same_provider_price_diagnostic_not_actual_fill_proof", "sor_eligibility_borrowing_inventory_and_short_extra_costs_unverified", "drawdown_realized_only_not_intratrade_mark_to_market", "leave_best_out_arithmetic_of_executed_book_no_freed_capacity_replay"}}
	r.ReplayClock = clock
	fail := func() (Report, error) {
		return r, errors.New("研究ブックのケース・時系列・固定した資金/費用条件を確認してください")
	}
	if len(cases) > 1000000 || len(allSessions) < 1 || len(allSessions) > 3660 || len(partition) < 1 || len(partition) > len(allSessions) || (s.FrictionBPS != 0 && s.FrictionBPS != 5 && s.FrictionBPS != 10 && s.FrictionBPS != 20) || (s.Commission != "sor_conditional_zero" && s.Commission != "non_sor_standard") || (s.ShortExtra != 0 && s.ShortExtra != 100 && s.ShortExtra != 500) {
		return fail()
	}
	calendar := map[string]bool{}
	monthDays := map[string]int{}
	for i, date := range allSessions {
		if !validDate(date) || (i > 0 && allSessions[i-1] >= date) {
			return fail()
		}
		calendar[date] = true
		monthDays[date[:7]]++
	}
	dates := map[string]bool{}
	dataFee := new(big.Rat)
	for i, date := range partition {
		if !calendar[date] || (i > 0 && partition[i-1] >= date) {
			return fail()
		}
		dates[date] = true
		dataFee.Add(dataFee, big.NewRat(7150, int64(monthDays[date[:7]])))
	}
	seen := map[string]bool{}
	rankSeen := map[string]map[int]bool{}
	candidates := []Case{}
	for _, c := range cases {
		if ctx.Err() != nil {
			return r, errors.New("研究ブックを中断しました")
		}
		key := c.Date + "/" + c.Symbol
		if !dates[c.Date] || !symbolPattern.MatchString(c.Symbol) || c.Rank < 1 || c.Rank > 30 || seen[key] {
			return fail()
		}
		seen[key] = true
		if rankSeen[c.Date] == nil {
			rankSeen[c.Date] = map[int]bool{}
		}
		if rankSeen[c.Date][c.Rank] {
			return fail()
		}
		rankSeen[c.Date][c.Rank] = true
		if c.Status != "trade" && c.Status != "no_trade" && c.Status != "unevaluable" {
			return fail()
		}
		if (c.Status == "trade") != (c.Trade != nil) || c.Common && c.Status == "unevaluable" {
			return fail()
		}
		r.Cases++
		if c.Common {
			r.Common++
		}
		if c.Status == "no_trade" {
			r.NoTrade++
		}
		if c.Status == "unevaluable" {
			r.Unevaluable++
		}
		if c.Trade != nil {
			t := c.Trade
			clockOK := t.ExitAt.After(t.EntryAt) && t.ExitAt.Sub(t.EntryAt) <= 20*time.Minute && !t.EntryAt.Before(marketdata.Clock(c.Date, 9, 6)) && !t.EntryAt.After(marketdata.Clock(c.Date, 10, 30)) && !t.ExitAt.After(marketdata.Clock(c.Date, 10, 50))
			if clock == Morning30 {
				clockOK = t.EntryAt.Equal(marketdata.Clock(c.Date, 9, 30)) && t.ExitAt.Equal(marketdata.Clock(c.Date, 10, 0))
			}
			if clock == Morning29 {
				clockOK = t.EntryAt.Equal(marketdata.Clock(c.Date, 9, 1)) && t.ExitAt.Equal(marketdata.Clock(c.Date, 9, 30))
			}
			if clock == Morning30VWAP {
				clockOK = t.EntryAt.Equal(marketdata.Clock(c.Date, 9, 30)) && t.ExitAt.After(t.EntryAt) && !t.ExitAt.After(marketdata.Clock(c.Date, 10, 0))
			}
			if (t.Direction != "long" && t.Direction != "short") || t.EntryPrice <= 0 || t.EntryPrice > marketdata.MaxPrice || t.ExitPrice <= 0 || t.ExitPrice > marketdata.MaxPrice || marketdata.Date(t.EntryAt) != c.Date || marketdata.Date(t.ExitAt) != c.Date || !clockOK || !t.EntryAt.Equal(t.EntryAt.Truncate(time.Minute)) || !t.ExitAt.Equal(t.ExitAt.Truncate(time.Minute)) {
				return fail()
			}
		}
		if s.CommonOnly && !c.Common {
			r.ExcludedCommon++
			continue
		}
		if c.Status == "trade" {
			candidates = append(candidates, c)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.Trade.EntryAt.Equal(b.Trade.EntryAt) {
			return a.Trade.EntryAt.Before(b.Trade.EntryAt)
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		return a.Symbol < b.Symbol
	})
	equity, peak, drawdown := integer(400000), integer(400000), integer(0)
	totals := make([]*big.Rat, 6)
	for i := range totals {
		totals[i] = new(big.Rat)
	}
	bySymbol := map[string]*big.Rat{}
	pending := -1
	settle := func() {
		if pending < 0 {
			return
		}
		entry := &r.Ledger[pending]
		net := exact(entry.Costs.Net)
		equity.Add(equity, net)
		entry.EquityAfter = equity.RatString()
		if net.Sign() > 0 {
			r.Winners++
		}
		if bySymbol[entry.Symbol] == nil {
			bySymbol[entry.Symbol] = new(big.Rat)
		}
		bySymbol[entry.Symbol].Add(bySymbol[entry.Symbol], net)
		values := []string{entry.Costs.Gross, entry.Costs.Commission, entry.Costs.Credit, entry.Costs.Friction, entry.Costs.ShortExtra, entry.Costs.Net}
		for i, v := range values {
			totals[i].Add(totals[i], exact(v))
		}
		if equity.Cmp(peak) > 0 {
			peak.Set(equity)
		}
		dd := new(big.Rat).Sub(peak, equity)
		if dd.Cmp(drawdown) > 0 {
			drawdown.Set(dd)
		}
		pending = -1
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return r, errors.New("研究ブックを中断しました")
		}
		r.Candidates++
		if pending >= 0 && !c.Trade.EntryAt.Before(r.Ledger[pending].Trade.ExitAt) {
			settle()
		}
		costs := quote(*c.Trade, s)
		fit := exact(costs.EntryBudget).Cmp(integer(400000)) <= 0
		if fit {
			r.IndependentFit++
		}
		e := Entry{Date: c.Date, Symbol: c.Symbol, Rank: c.Rank, Trade: *c.Trade, Costs: costs, EquityBefore: equity.RatString(), FitsIndependent: fit}
		if pending >= 0 {
			e.Status = "skipped_busy"
			r.Busy++
		} else if exact(costs.EntryBudget).Cmp(equity) > 0 {
			e.Status = "skipped_capital"
			r.Capital++
		} else {
			e.Status = "executed"
			r.Executed++
		}
		r.Ledger = append(r.Ledger, e)
		if e.Status == "executed" {
			pending = len(r.Ledger) - 1
		}
	}
	settle()
	r.EndEquity = equity.RatString()
	r.Drawdown = drawdown.RatString()
	r.Gross = totals[0].RatString()
	r.Commission = totals[1].RatString()
	r.Credit = totals[2].RatString()
	r.Friction = totals[3].RatString()
	r.ShortExtra = totals[4].RatString()
	r.Net = totals[5].RatString()
	r.DataFee = dataFee.RatString()
	netAfter := new(big.Rat).Sub(totals[5], dataFee)
	r.AfterData = netAfter.RatString()
	positive, best := new(big.Rat), new(big.Rat)
	for symbol, v := range bySymbol {
		r.SymbolNet[symbol] = v.RatString()
		if v.Sign() > 0 {
			positive.Add(positive, v)
			if v.Cmp(best) > 0 {
				best.Set(v)
			}
		}
	}
	r.LargestPositiveShare = "0"
	if positive.Sign() > 0 {
		r.LargestPositiveShare = new(big.Rat).Quo(best, positive).RatString()
	}
	loo := new(big.Rat).Sub(netAfter, best)
	r.LeaveBestOut = loo.RatString()
	r.Decision = "paper_research_candidate"
	if r.Executed < 30 {
		r.GateReasons = append(r.GateReasons, "fewer_than_30_executed_trades")
	}
	if r.Cases == 0 || r.Common*5 < r.Cases*4 {
		r.GateReasons = append(r.GateReasons, "common_case_coverage_below_80pct")
	}
	if len(r.GateReasons) > 0 {
		r.Decision = "hold"
	}
	if netAfter.Sign() <= 0 {
		r.Decision = "reject_this_version"
		r.GateReasons = append(r.GateReasons, "nonpositive_net_after_allocated_data_fee")
	}
	if exact(r.LargestPositiveShare).Cmp(big.NewRat(1, 2)) > 0 {
		r.GateReasons = append(r.GateReasons, "single_stock_exceeds_half_positive_net")
		if r.Decision != "reject_this_version" {
			r.Decision = "hold"
		}
	}
	if loo.Sign() <= 0 {
		r.GateReasons = append(r.GateReasons, "leave_best_executed_symbol_out_not_positive")
		if r.Decision != "reject_this_version" {
			r.Decision = "hold"
		}
	}
	return r, nil
}
