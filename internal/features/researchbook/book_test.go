package researchbook

import (
	"context"
	"encoding/json"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"reflect"
	"testing"
)

func testTrade(symbol string, rank, minute int, entry, exit int64) Case {
	date := "2026-06-30"
	return Case{Date: date, Symbol: symbol, Rank: rank, Status: "trade", Common: true, Reasons: []string{}, Trade: &Trade{Direction: "long", EntryAt: marketdata.Clock(date, 9, minute), ExitAt: marketdata.Clock(date, 9, minute+1), EntryPrice: entry * marketdata.PriceScale, ExitPrice: exit * marketdata.PriceScale, Reason: "fabricated_test"}}
}
func standard() Scenario {
	return Scenario{FrictionBPS: 10, Commission: "non_sor_standard", ShortExtra: 100}
}

func TestFixedMorningWindowsShareCostsWithoutRelaxingLegacy(t *testing.T) {
	for _, clock := range []string{Morning30, Morning29} {
		a := testTrade("0000@1", 1, 30, 5000, 5050)
		if clock == Morning30 {
			a.Trade.EntryAt = marketdata.Clock(a.Date, 9, 30)
			a.Trade.ExitAt = marketdata.Clock(a.Date, 10, 0)
		} else {
			a.Trade.EntryAt = marketdata.Clock(a.Date, 9, 1)
			a.Trade.ExitAt = marketdata.Clock(a.Date, 9, 30)
		}
		b := a
		b.Symbol = "0001@1"
		b.Rank = 2
		r, err := RunTimedMorning(context.Background(), []Case{b, a}, []string{a.Date}, []string{a.Date}, standard(), clock)
		if err != nil || r.Executed != 1 || r.Busy != 1 || r.Net != "3878" || r.ReplayClock != clock || r.TradingEligible || r.BorrowingVerified {
			t.Fatal("morning book costs/capacity", err)
		}
		if _, err := Run(context.Background(), []Case{a}, []string{a.Date}, []string{a.Date}, standard()); err == nil {
			t.Fatal("legacy clock relaxed")
		}
		a.Trade.ExitAt = a.Trade.ExitAt.AddDate(0, 0, 1)
		if _, err := RunTimedMorning(context.Background(), []Case{a}, []string{a.Date}, []string{a.Date}, standard(), clock); err == nil {
			t.Fatal("fixed morning allowed another exit")
		}
	}
}
func TestCostsCommissionBandsAndEntryOnlyReserve(t *testing.T) {
	c := testTrade("0000@1", 1, 6, 5000, 5050)
	r, err := Run(context.Background(), []Case{c}, []string{c.Date}, []string{c.Date}, standard())
	if err != nil || r.Executed != 1 || r.Gross != "5000" || r.Commission != "583" || r.Credit != "39" || r.Friction != "500" || r.ShortExtra != "0" || r.Net != "3878" || r.EndEquity != "403878" || r.Ledger[0].Costs.EntryBudget != "300935" || r.TradingEligible || r.StrategyAdopted || r.BorrowingVerified {
		t.Fatal("long accounting changed", err)
	}
	c.Trade.Direction = "short"
	c.Trade.ExitPrice = 4950 * marketdata.PriceScale
	r, err = Run(context.Background(), []Case{c}, []string{c.Date}, []string{c.Date}, standard())
	if err != nil || r.Net != "3988" || r.Credit != "16" || r.Commission != "396" || r.ShortExtra != "100" || r.Ledger[0].Costs.EntryBudget != "301012" {
		t.Fatal("short accounting changed", err)
	}
	for _, p := range []struct{ price, fee int64 }{{1000, 99}, {1001, 148}, {2000, 148}, {2001, 198}, {5000, 198}, {5001, 385}} {
		if commission(notional(p.price*marketdata.PriceScale), "non_sor_standard").RatString() != integer(p.fee).RatString() {
			t.Fatal("commission band boundary changed")
		}
	}
	a := quote(*c.Trade, standard())
	c.Trade.ExitPrice = 100 * marketdata.PriceScale
	b := quote(*c.Trade, standard())
	if a.EntryBudget != b.EntryBudget {
		t.Fatal("future exit price changed entry affordability")
	}
}
func TestChronologicalCashBusyTiesAndSameTimestampExit(t *testing.T) {
	a := testTrade("0000@1", 1, 6, 5000, 5050)
	b := testTrade("0001@1", 2, 6, 5000, 7000)
	c := testTrade("0002@1", 3, 7, 5000, 5050)
	cases := []Case{c, b, a}
	before, _ := json.Marshal(cases)
	r, err := Run(context.Background(), cases, []string{a.Date}, []string{a.Date}, standard())
	after, _ := json.Marshal(cases)
	if err != nil || r.Executed != 2 || r.Busy != 1 || r.Capital != 0 || r.Ledger[0].Symbol != a.Symbol || r.Ledger[1].Symbol != b.Symbol || r.Ledger[1].Status != "skipped_busy" || r.Ledger[1].EquityBefore != "400000" || r.Ledger[2].EquityBefore != "403878" || r.Net != "7756" || string(before) != string(after) {
		t.Fatal("chronological equity or ties changed", err)
	}
	reversed, err := Run(context.Background(), []Case{a, b, c}, []string{a.Date}, []string{a.Date}, standard())
	if err != nil || !reflect.DeepEqual(r, reversed) {
		t.Fatal("input order changed research book")
	}
	a.Trade.ExitPrice = 1000 * marketdata.PriceScale
	r, err = Run(context.Background(), []Case{a, c}, []string{a.Date}, []string{a.Date}, standard())
	if err != nil || r.Executed != 1 || r.Capital != 1 || r.Ledger[1].FitsIndependent != true {
		t.Fatal("realized loss did not constrain later entry", err)
	}
}
func TestFixedFeeAllocationCoverageAndCommonCases(t *testing.T) {
	dates := []string{"2026-06-29", "2026-06-30", "2026-07-01"}
	a := testTrade("0000@1", 1, 6, 5000, 5050)
	a.Common = false
	b := testTrade("0001@1", 2, 7, 5000, 5050)
	s := standard()
	s.CommonOnly = true
	r, err := Run(context.Background(), []Case{a, b}, dates, []string{"2026-06-30"}, s)
	if err != nil || r.Cases != 2 || r.Common != 1 || r.ExcludedCommon != 1 || r.Executed != 1 || r.DataFee != "3575" || r.AfterData != "303" || r.LeaveBestOut != "-3575" || r.LargestPositiveShare != "1" || r.Decision != "hold" {
		t.Fatal("common coverage or fee allocation changed", err)
	}
	june, err := Run(context.Background(), nil, dates, dates[:2], standard())
	if err != nil || june.DataFee != "7150" {
		t.Fatal("June fee not exactly monthly")
	}
	july, err := Run(context.Background(), nil, dates, dates[2:], standard())
	if err != nil || july.DataFee != "7150" {
		t.Fatal("July fee not exactly monthly")
	}
	a.Status = "unevaluable"
	a.Trade = nil
	r, err = Run(context.Background(), []Case{a, b}, dates, []string{"2026-06-30"}, s)
	if err != nil || r.Unevaluable != 1 || r.NoTrade != 0 || r.Cases != 2 {
		t.Fatal("missing case became zero-trade case")
	}
}
func TestInvalidCasesBoundsAndCancellation(t *testing.T) {
	for _, fault := range []string{"duplicate", "rank", "status", "common_missing", "price", "time", "future_partition", "scenario"} {
		a := testTrade("0000@1", 1, 6, 5000, 5050)
		cases := []Case{a}
		s := standard()
		partition := []string{a.Date}
		switch fault {
		case "duplicate":
			cases = append(cases, a)
		case "rank":
			cases[0].Rank = 31
		case "status":
			cases[0].Status = "pretend_fill"
		case "common_missing":
			cases[0].Status = "unevaluable"
			cases[0].Trade = nil
		case "price":
			cases[0].Trade.EntryPrice = marketdata.MaxPrice + 1
		case "time":
			cases[0].Trade.ExitAt = cases[0].Trade.EntryAt
		case "future_partition":
			partition = []string{"2026-07-01"}
		case "scenario":
			s.FrictionBPS = 11
		}
		if _, err := Run(context.Background(), cases, []string{a.Date}, partition, s); err == nil {
			t.Fatal("invalid research input accepted", fault)
		}
	}
	a := testTrade("0000@1", 1, 6, 5000, 5050)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, []Case{a}, []string{a.Date}, []string{a.Date}, standard()); err == nil {
		t.Fatal("cancel ignored")
	}
}
