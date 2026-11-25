package assesscapital

import (
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func fixture() (Assumptions, Trade) {
	at := marketdata.Clock("2026-10-16", 9, 20)
	return Assumptions{CapitalJPY: 400000, Quantity: 100, LotSize: 100, Mode: "cash", InitialMarginBPS: 10000, FrictionBPS: 10, CommissionJPY: 20}, Trade{Direction: "long", EntryAt: at, ExitAt: at.Add(20 * time.Minute), EntryPrice: 3000 * marketdata.PriceScale, ExitPrice: 2990 * marketdata.PriceScale}
}

func TestCashReservesWholeLotAndRoundTripCosts(t *testing.T) {
	a, trade := fixture()
	r, err := Assess(a, trade)
	if err != nil || !r.FitsAssumedBudget || r.EntryNotionalJPY != "300000.0000" || r.InitialReserveJPY != 300000 || r.FrictionJPY != 300 || r.RequiredBudgetJPY != 300320 || r.GrossJPY != "-1000.0000" || r.NetJPY != "-1320.0000" || r.EndEquityJPY != "398680.0000" || r.TradingEligible || r.AccountVerified || r.PortfolioMode {
		t.Fatal("cash accounting", r, err)
	}
	a.CapitalJPY = r.RequiredBudgetJPY
	if r, err := Assess(a, trade); err != nil || !r.FitsAssumedBudget || r.BudgetAfterReservationJPY != 0 {
		t.Fatal("exact budget rejected")
	}
	a.CapitalJPY--
	if r, err := Assess(a, trade); err != nil || r.FitsAssumedBudget || r.NetJPY != "-1320.0000" || len(r.Reasons) != 1 {
		t.Fatal("unaffordable loss erased")
	}
}

func TestMarginShortUsesCollateralWithoutCreditingSaleProceeds(t *testing.T) {
	a, trade := fixture()
	a.Mode = "margin"
	a.InitialMarginBPS = 3000
	a.MinimumCollateralJPY = 300000
	a.CommissionJPY = 0
	a.CreditCostsJPY = 100
	trade.Direction = "short"
	trade.EntryPrice = 10000 * marketdata.PriceScale
	trade.ExitPrice = 9900 * marketdata.PriceScale
	r, err := Assess(a, trade)
	if err != nil || !r.FitsAssumedBudget || r.InitialReserveJPY != 300000 || r.RequiredBudgetJPY != 301100 || r.BudgetAfterReservationJPY != 98900 || r.GrossJPY != "10000.0000" || r.NetJPY != "8900.0000" || r.ShortAvailabilityVerified || r.TradingEligible {
		t.Fatal("short accounting", r, err)
	}
	a.CapitalJPY = 299999
	trade.EntryPrice = 1000 * marketdata.PriceScale
	trade.ExitPrice = 999 * marketdata.PriceScale
	r, err = Assess(a, trade)
	if err != nil || r.FitsAssumedBudget || strings.Join(r.Reasons, ",") != "below_assumed_minimum_collateral" {
		t.Fatal("minimum collateral ignored", r, err)
	}
}

func TestFractionalPricesRoundOnlyReserveAndFrictionUp(t *testing.T) {
	a, trade := fixture()
	trade.EntryPrice = 1234567
	trade.ExitPrice = 1234568
	a.CommissionJPY = 0
	a.FrictionBPS = 5
	r, err := Assess(a, trade)
	if err != nil || r.EntryNotionalJPY != "12345.6700" || r.GrossJPY != "0.0100" || r.InitialReserveJPY != 12346 || r.FrictionJPY != 7 || r.NetJPY != "-6.9900" {
		t.Fatal("fractional arithmetic", r, err)
	}
}

func TestInvalidAndUnsupportedCases(t *testing.T) {
	for _, kind := range []string{"lot", "quantity", "margin", "fee", "cash_credit_fee", "overnight", "time", "price", "direction"} {
		t.Run(kind, func(t *testing.T) {
			a, trade := fixture()
			switch kind {
			case "lot":
				a.Quantity = 99
			case "quantity":
				a.Quantity = 1000001
			case "margin":
				a.InitialMarginBPS = 0
			case "fee":
				a.FrictionBPS = -1
			case "cash_credit_fee":
				a.CreditCostsJPY = 1
			case "overnight":
				trade.ExitAt = trade.ExitAt.Add(24 * time.Hour)
			case "time":
				trade.ExitAt = trade.EntryAt
			case "price":
				trade.EntryPrice = marketdata.MaxPrice + 1
			case "direction":
				trade.Direction = "unknown"
			}
			if _, err := Assess(a, trade); err == nil {
				t.Fatal("invalid case accepted")
			}
		})
	}
	a, trade := fixture()
	trade.Direction = "short"
	r, err := Assess(a, trade)
	if err != nil || r.FitsAssumedBudget || r.Reasons[0] != "cash_short_not_supported" || r.NetJPY != "" {
		t.Fatal("cash short calculated")
	}
}

func TestLargeNotionalAvoidsIntermediateOverflow(t *testing.T) {
	a, trade := fixture()
	a.Quantity = 1000000
	a.FrictionBPS = 10000
	a.CommissionJPY = 0
	trade.EntryPrice = marketdata.MaxPrice
	trade.ExitPrice = 1
	r, err := Assess(a, trade)
	if err != nil || r.InitialReserveJPY != 100000000000000 || r.FrictionJPY != 100000000000000 || r.NetJPY != "-199999999999900.0000" {
		t.Fatal("large arithmetic", r, err)
	}
}
