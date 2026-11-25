// Package assesscapital checks one hypothetical same-day trade at a time.
// It has no wallet, broker, file or order interface and never authorizes trading.
package assesscapital

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

const Version = "independent-trade-capital-v0.1"

// Every value is a research assumption. Fees are whole-yen, round-trip costs;
// friction represents spread/slippage separately from commission and credit fees.
type Assumptions struct {
	CapitalJPY           int64  `json:"capital_jpy"`
	Quantity             int64  `json:"quantity"`
	LotSize              int64  `json:"lot_size"`
	Mode                 string `json:"mode"`
	InitialMarginBPS     int64  `json:"initial_margin_bps"`
	MinimumCollateralJPY int64  `json:"minimum_collateral_jpy"`
	FrictionBPS          int64  `json:"execution_friction_bps"`
	CommissionJPY        int64  `json:"round_trip_commission_jpy"`
	CreditCostsJPY       int64  `json:"round_trip_credit_cost_jpy"`
}

func Validate(a Assumptions) error {
	if a.CapitalJPY < 1 || a.CapitalJPY > 1e12 || a.Quantity < 1 || a.Quantity > 1e6 || a.LotSize < 1 || a.LotSize > 1e6 || a.Quantity%a.LotSize != 0 || a.InitialMarginBPS < 1 || a.InitialMarginBPS > 10000 || a.MinimumCollateralJPY < 0 || a.MinimumCollateralJPY > 1e12 || a.FrictionBPS < 0 || a.FrictionBPS > 10000 || a.CommissionJPY < 0 || a.CommissionJPY > 1e9 || a.CreditCostsJPY < 0 || a.CreditCostsJPY > 1e9 {
		return errors.New("資金・単元/数量・保証金・費用の仮定を確認してください")
	}
	if a.Mode != "cash" && a.Mode != "margin" || a.Mode == "cash" && (a.InitialMarginBPS != 10000 || a.MinimumCollateralJPY != 0 || a.CreditCostsJPY != 0) {
		return errors.New("modeはcashまたはmargin。cashは100%資金・最低保証金0・信用費用0で指定してください")
	}
	return nil
}

type Trade struct {
	Direction             string
	EntryAt, ExitAt       time.Time
	EntryPrice, ExitPrice int64 // marketdata.PriceScale units per share.
}

type Assessment struct {
	Version                   string   `json:"version"`
	FitsAssumedBudget         bool     `json:"fits_assumed_budget"`
	Reasons                   []string `json:"reasons"`
	EntryNotionalJPY          string   `json:"entry_notional_jpy"`
	InitialReserveJPY         int64    `json:"initial_reserve_jpy"`
	FrictionJPY               int64    `json:"execution_friction_jpy"`
	CommissionJPY             int64    `json:"round_trip_commission_jpy"`
	CreditCostsJPY            int64    `json:"round_trip_credit_cost_jpy"`
	TotalCostsJPY             int64    `json:"total_costs_jpy"`
	RequiredBudgetJPY         int64    `json:"required_budget_jpy"`
	BudgetAfterReservationJPY int64    `json:"budget_after_reservation_jpy"`
	GrossJPY                  string   `json:"hypothetical_gross_jpy"`
	NetJPY                    string   `json:"hypothetical_net_jpy"`
	EndEquityJPY              string   `json:"hypothetical_end_equity_jpy"`
	TradingEligible           bool     `json:"trading_eligible"`
	AccountVerified           bool     `json:"account_verified"`
	ShortAvailabilityVerified bool     `json:"short_availability_verified"`
	PortfolioMode             bool     `json:"portfolio_mode"`
}

func Assess(a Assumptions, t Trade) (Assessment, error) {
	r := Assessment{Version: Version, Reasons: []string{}}
	if err := Validate(a); err != nil {
		return r, err
	}
	if t.Direction != "long" && t.Direction != "short" || t.EntryPrice <= 0 || t.EntryPrice > marketdata.MaxPrice || t.ExitPrice <= 0 || t.ExitPrice > marketdata.MaxPrice || t.EntryAt.IsZero() || !t.ExitAt.After(t.EntryAt) || marketdata.Date(t.EntryAt) != marketdata.Date(t.ExitAt) {
		return r, errors.New("同日中の方向・取得/退出時刻・価格を確認してください")
	}
	if a.Mode == "cash" && t.Direction == "short" {
		r.Reasons = append(r.Reasons, "cash_short_not_supported")
		return r, nil
	}
	// The bounds above keep scaled notional and signed P&L inside int64.
	notional := t.EntryPrice * a.Quantity
	margin := a.InitialMarginBPS
	reserve := ceilProduct(notional, margin, marketdata.PriceScale*10000)
	friction := ceilProduct(notional, a.FrictionBPS, marketdata.PriceScale*10000)
	r.EntryNotionalJPY = money(notional)
	r.InitialReserveJPY, r.FrictionJPY = reserve, friction
	r.CommissionJPY, r.CreditCostsJPY = a.CommissionJPY, a.CreditCostsJPY
	r.TotalCostsJPY = friction + a.CommissionJPY + a.CreditCostsJPY
	r.RequiredBudgetJPY = reserve + r.TotalCostsJPY
	r.BudgetAfterReservationJPY = a.CapitalJPY - r.RequiredBudgetJPY
	if a.CapitalJPY < a.MinimumCollateralJPY {
		r.Reasons = append(r.Reasons, "below_assumed_minimum_collateral")
	}
	if r.RequiredBudgetJPY > a.CapitalJPY {
		r.Reasons = append(r.Reasons, "insufficient_assumed_budget")
	}
	r.FitsAssumedBudget = len(r.Reasons) == 0
	gross := (t.ExitPrice - t.EntryPrice) * a.Quantity
	if t.Direction == "short" {
		gross = -gross
	}
	net := gross - r.TotalCostsJPY*marketdata.PriceScale
	r.GrossJPY, r.NetJPY = money(gross), money(net)
	r.EndEquityJPY = money(a.CapitalJPY*marketdata.PriceScale + net)
	// A failed budget still keeps the hypothetical outcome for diagnosis. It is
	// never silently dropped to improve results or counted as an executed trade.
	return r, nil
}

func ceilProduct(n, multiplier, divisor int64) int64 {
	v := new(big.Int).Mul(big.NewInt(n), big.NewInt(multiplier))
	v.Add(v, big.NewInt(divisor-1))
	return v.Quo(v, big.NewInt(divisor)).Int64()
}

func money(scaled int64) string {
	negative := scaled < 0
	if negative {
		scaled = -scaled
	}
	value := fmt.Sprintf("%d.%04d", scaled/marketdata.PriceScale, scaled%marketdata.PriceScale)
	if negative {
		return "-" + value
	}
	return value
}
