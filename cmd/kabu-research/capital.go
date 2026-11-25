package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/assesscapital"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/backtest"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

type capitalDay struct {
	Source     backtest.Result           `json:"source_result"`
	Assessment *assesscapital.Assessment `json:"assessment,omitempty"`
}

type capitalReport struct {
	Version           string                    `json:"version"`
	Scope             string                    `json:"scope"`
	InputFile         string                    `json:"input_file"`
	InputSHA256       string                    `json:"input_sha256"`
	AssumptionsSHA256 string                    `json:"assumptions_sha256"`
	Assumptions       assesscapital.Assumptions `json:"assumptions"`
	Symbol            string                    `json:"symbol"`
	Direction         r001.Direction            `json:"direction"`
	Variant           r001.Variant              `json:"variant"`
	Rule              string                    `json:"strategy_version"`
	SourceScope       string                    `json:"source_scope"`
	SourceLimitations []string                  `json:"source_limitations"`
	Days              []capitalDay              `json:"days"`
	TradingEligible   bool                      `json:"trading_eligible"`
	StrategyAdopted   bool                      `json:"strategy_adopted"`
	PortfolioMode     bool                      `json:"portfolio_mode"`
}

func runCapitalCheck(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("capital-check", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "既存のdiagnostic/backtest JSON。価格仮定を継承し、取引許可には使わない")
	assumptions := fs.String("assumptions", "", "必須: すべての資金・単元・費用仮定を明記したJSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *input == "" || *assumptions == "" {
		return errors.New("capital-checkにはinputとassumptionsを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	assumptionRaw, err := readInput(*assumptions)
	if err != nil {
		return err
	}
	a, err := decodeCapitalAssumptions(assumptionRaw)
	if err != nil {
		return err
	}
	var source backtest.Report
	if json.Unmarshal(raw, &source) != nil {
		return errors.New("研究結果JSONの形式を確認してください")
	}
	report, err := assessResearchCapital(ctx, source, a)
	if err != nil {
		return err
	}
	report.InputFile, report.InputSHA256, report.AssumptionsSHA256 = filepath.Base(*input), hash(raw), hash(assumptionRaw)
	path, outputRaw, err := save("capital-assessment", report)
	if err != nil {
		return err
	}
	manifest, err := recordRun("capital-check", filepath.Base(*input), raw, hash(raw), path, outputRaw, struct {
		Version, AssumptionsFile, AssumptionsSHA256 string
		Assumptions                                 assesscapital.Assumptions
	}{assesscapital.Version, filepath.Base(*assumptions), hash(assumptionRaw), a})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "資金仮定検査: strategy=%s / symbol=%s / mode=%s / independent_trades=true\n", report.Rule, report.Symbol, a.Mode); err != nil {
		return errors.New("資金検査結果を出力できません")
	}
	for _, day := range report.Days {
		if day.Assessment == nil {
			if _, err := fmt.Fprintf(out, "date=%s / status=%s / reasons=%v\n", day.Source.Date, day.Source.Status, day.Source.Reasons); err != nil {
				return errors.New("資金検査結果を出力できません")
			}
			continue
		}
		r := day.Assessment
		if _, err := fmt.Fprintf(out, "date=%s / status=%s / fits_assumed_budget=%t / required_jpy=%d / hypothetical_gross_jpy=%s / costs_jpy=%d / hypothetical_net_jpy=%s / reasons=%v\n", day.Source.Date, day.Source.Status, r.FitsAssumedBudget, r.RequiredBudgetJPY, r.GrossJPY, r.TotalCostsJPY, r.NetJPY, r.Reasons); err != nil {
			return errors.New("資金検査結果を出力できません")
		}
	}
	_, err = fmt.Fprintf(out, "trading_eligible=false / strategy_adopted=false / portfolio_mode=false / actual_credit_and_costs_unverified\n保存: %s\n実行記録: %s\n", path, manifest)
	return err
}

func decodeCapitalAssumptions(raw []byte) (assesscapital.Assumptions, error) {
	var a assesscapital.Assumptions
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return a, errors.New("資金仮定JSONを確認してください")
	}
	for _, key := range []string{"capital_jpy", "quantity", "lot_size", "mode", "initial_margin_bps", "minimum_collateral_jpy", "execution_friction_bps", "round_trip_commission_jpy", "round_trip_credit_cost_jpy"} {
		if fields[key] == nil || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return a, errors.New("資金仮定は費用0を含め全項目の明示が必要です")
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil {
		return a, errors.New("資金仮定の項目と整数値を確認してください")
	}
	return a, assesscapital.Validate(a)
}

func assessResearchCapital(ctx context.Context, source backtest.Report, a assesscapital.Assumptions) (capitalReport, error) {
	r := capitalReport{Version: assesscapital.Version, Scope: "independent_trade_funding_sensitivity", Assumptions: a, Symbol: source.Symbol, Direction: source.Direction, Variant: source.Variant, Rule: source.Rule, SourceScope: source.Scope, SourceLimitations: []string{"next_minute_open_execution_assumed", "actual_credit_inventory_and_fees_unverified", "independent_trade_budgets_not_portfolio", "intratrade_drawdown_and_margin_calls_not_modeled"}, Days: []capitalDay{}}
	if err := assesscapital.Validate(a); err != nil {
		return r, err
	}
	if !r001.Valid(source.Direction) || !r001.ValidVariant(source.Variant) || source.Rule != r001.VersionFor(source.Direction, source.Variant) || source.Model != backtest.ModelVersion || source.PriceScale != marketdata.PriceScale || !regexp.MustCompile(`^[0-9A-Z]{4}@(1|3|5|6)$`).MatchString(source.Symbol) || len(source.Days) == 0 || source.StrategyAdopted || source.PortfolioMode || source.ShortAvailabilityVerified {
		return r, errors.New("未採用の研究結果の版・方向・銘柄・価格単位を確認してください")
	}
	switch source.Scope {
	case "crosschecked_price_pattern_diagnostic":
		if source.Version != 2 || source.Diagnostic == nil || source.Diagnostic.Version != backtest.DiagnosticVersion || len(source.Diagnostic.Days) != len(source.Days) {
			return r, errors.New("価格診断の照合根拠を確認してください")
		}
		r.SourceLimitations = append(r.SourceLimitations, source.Diagnostic.Limitations...)
	case "synthetic_logic_test":
		if source.Version != 1 || source.Symbol != "0000@1" {
			return r, errors.New("合成研究結果の形式を確認してください")
		}
	default:
		return r, errors.New("capital-checkは照合付き価格診断または合成backtest結果に限定します")
	}
	previous := ""
	for i, day := range source.Days {
		if ctx.Err() != nil {
			return r, errors.New("資金検査を中断しました")
		}
		if day.Date <= previous {
			return r, errors.New("研究結果の日付重複・順序を確認してください")
		}
		previous = day.Date
		entry := capitalDay{Source: day}
		switch day.Status {
		case "trade":
			if day.Trade == nil || marketdata.Date(day.Trade.EntryAt) != day.Date || source.Diagnostic != nil && (source.Diagnostic.Days[i].Date != day.Date || !source.Diagnostic.Days[i].Eligible || source.Diagnostic.Days[i].Matched != 111 || source.Diagnostic.Days[i].Required != 111) {
				return r, errors.New("取引と照合根拠の日付・範囲を確認してください")
			}
			t := day.Trade
			assessment, err := assesscapital.Assess(a, assesscapital.Trade{Direction: string(source.Direction), EntryAt: t.EntryAt, ExitAt: t.ExitAt, EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice})
			if err != nil {
				return r, err
			}
			entry.Assessment = &assessment
		case "no_signal", "filtered", "unevaluable":
			if day.Trade != nil {
				return r, errors.New("非取引日の取引情報を確認してください")
			}
		default:
			return r, errors.New("研究結果の状態を確認してください")
		}
		r.Days = append(r.Days, entry)
	}
	return r, nil
}
