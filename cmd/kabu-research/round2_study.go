package main

import (
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtests01"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/prepareorb"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/researchbook"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

type round2Cohorts struct {
	Legacy bool            `json:"legacy_five_common"`
	New    bool            `json:"new_three_common"`
	All    bool            `json:"all_eight_common"`
	Pairs  map[string]bool `json:"legacy_s01_candidate_pair_common"`
}

type round2GateReport struct {
	ScopeSHA string   `json:"scope_plan_sha256"`
	Decision string   `json:"research_gate"`
	Reasons  []string `json:"gate_reasons"`
}

// Restore the original prefix policy for the old S01 comparison. A P0 RVOL
// must never silently replace the old rule's missing historical references.
func round2LegacyInput(c s01InputCase) (s01InputCase, error) {
	if c.LegacyOpening == nil || c.LegacyAvailable == nil || c.LegacyRvolPassed == nil || c.Opening.OpeningPolicy != prepareorb.PastVolumePolicy || c.LegacyOpening.OpeningPolicy != prepareorb.JQuantsPrefixPolicy || c.LegacyOpening.QualityPromoted || c.LegacyOpening.TradingEligible || c.LegacyOpening.StrategyAdopted || c.LegacyOpening.Symbol != c.Opening.Symbol || c.LegacyOpening.Target.Date != c.Date {
		return s01InputCase{}, errors.New("旧S01の未変更の寄り入力と品質を確認してください")
	}
	legacy := c
	legacy.Opening = *c.LegacyOpening
	legacy.Problems = []string{}
	if !legacy.Opening.CalculationAvailable {
		legacy.Problems = append(legacy.Problems, "opening_rvol_unavailable")
	}
	if !c.ATR.Available || c.ATR.StopDistance == nil || *c.ATR.StopDistance <= 0 {
		legacy.Problems = append(legacy.Problems, "atr_or_positive_stop_unavailable")
	}
	legacy.InputsAvailable = len(legacy.Problems) == 0 && len(c.ExPostIssues) == 0
	legacy.RvolPassed = legacy.Opening.AtLeastOne != nil && *legacy.Opening.AtLeastOne
	if legacy.InputsAvailable != *c.LegacyAvailable || legacy.RvolPassed != *c.LegacyRvolPassed {
		return s01InputCase{}, errors.New("保存した旧指標の利用可否が一致しません")
	}
	return legacy, nil
}

func round2PriceCase(ctx context.Context, d marketdata.Dataset, c s01InputCase, ref marketdata.DailyBar, id string) (researchbook.Case, error) {
	row := researchbook.Case{Date: c.Date, Symbol: d.Symbol, Rank: c.LiquidityRank, Status: "unevaluable", Reasons: append(append([]string{}, c.Problems...), c.ExPostIssues...)}
	if _, err := s01.Round2Rule(id); err != nil {
		return row, err
	}
	if !c.InputsAvailable {
		return row, nil
	}
	if c.Opening.Target.Range == nil || c.Opening.RelativeVolume == nil || !c.Opening.CalculationAvailable || c.Opening.OpeningPolicy != prepareorb.PastVolumePolicy || c.ATR.Value == nil || !c.ATR.Available || c.ATR.Symbol != d.Symbol || c.ATR.Date != c.Date || c.Opening.Symbol != d.Symbol || c.Opening.Target.Date != c.Date {
		return row, errors.New("第2回研究の固定指標と出典を確認してください")
	}
	o := c.Opening.Target.Range
	r, err := backtests01.DiagnoseRound2(ctx, d, c.Date, ref, s01.Range{Open: o.Open, High: o.High, Low: o.Low, Close: o.Close}, *c.ATR.Value, *c.Opening.RelativeVolume, id)
	if err != nil {
		return row, err
	}
	row.Status, row.Reasons = r.Status, r.Reasons
	if row.Status == "traded" {
		row.Status = "trade"
	}
	if r.Trade != nil {
		t := r.Trade
		row.Trade = &researchbook.Trade{Direction: string(t.Direction), EntryAt: t.EntryAt, ExitAt: t.ExitAt, EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice, Reason: t.Reason}
	}
	return row, nil
}

func round2CaseCohorts(rows []researchbook.Case) (round2Cohorts, error) {
	c := round2Cohorts{Legacy: true, New: true, All: true, Pairs: map[string]bool{}}
	if len(rows) != 8 {
		return c, errors.New("旧5/新3モデルの比較列を確認してください")
	}
	for i, row := range rows {
		if row.Date != rows[0].Date || row.Symbol != rows[0].Symbol || row.Rank != rows[0].Rank {
			return c, errors.New("共通比較の日付/銘柄/順位を確認してください")
		}
		evaluable := row.Status != "unevaluable"
		c.All = c.All && evaluable
		if i < 5 {
			c.Legacy = c.Legacy && evaluable
		} else {
			c.New = c.New && evaluable
			c.Pairs[[]string{"R2-P0", "R2-P1", "R2-P2"}[i-5]] = rows[0].Status != "unevaluable" && evaluable
		}
	}
	return c, nil
}

func round2BookCohorts(model string) []string {
	names := []string{"native", "all_eight"}
	if model == s01.Version {
		return append(names, "pair_R2-P0", "pair_R2-P1", "pair_R2-P2")
	}
	if strings.HasPrefix(model, "R2-") {
		return append(names, "pair_"+model)
	}
	return names
}

func round2SelectCohort(cases []researchbook.Case, cohorts map[string]round2Cohorts, name string) []researchbook.Case {
	out := append([]researchbook.Case{}, cases...)
	for i := range out {
		c := cohorts[out[i].Date+"/"+out[i].Symbol]
		out[i].Common = c.All
		if strings.HasPrefix(name, "pair_") {
			out[i].Common = c.Pairs[strings.TrimPrefix(name, "pair_")]
		}
	}
	return out
}

// The old report/gates stay intact. This separately named gate adds the
// preregistered own-coverage and 40,000-yen drawdown limits to new candidates.
func round2Gate(r researchbook.Report) (round2GateReport, error) {
	g := round2GateReport{ScopeSHA: s01.Round2ScopeSHA256, Decision: r.Decision, Reasons: append([]string{}, r.GateReasons...)}
	dd, ok := new(big.Rat).SetString(r.Drawdown)
	if !ok || dd.Sign() < 0 {
		return g, errors.New("第2回研究の実現ドローダウンを確認してください")
	}
	if r.Cases == 0 || (r.Cases-r.Unevaluable)*5 < r.Cases*4 {
		g.Reasons = append(g.Reasons, "candidate_case_coverage_below_80pct")
	}
	if dd.Cmp(big.NewRat(40000, 1)) > 0 {
		g.Reasons = append(g.Reasons, "realized_drawdown_exceeds_40000_jpy_research_limit")
	}
	if len(g.Reasons) > 0 && g.Decision != "reject_this_version" {
		g.Decision = "hold"
	}
	return g, nil
}
