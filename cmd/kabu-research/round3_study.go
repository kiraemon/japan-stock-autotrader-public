package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/backtestintraday"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/backtests01"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/prepareorb"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/researchbook"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/round3"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/s01"
)

var round3Models = []string{s01.Version, round3.NoiseRule, round3.GapRule}

type round3GateReport struct {
	ScopeSHA string   `json:"scope_plan_sha256"`
	Decision string   `json:"research_gate"`
	Reasons  []string `json:"gate_reasons"`
}
type round3MatrixRow struct {
	Date    string                  `json:"date_jst"`
	Rank    int                     `json:"liquidity_rank"`
	Opening prepareorb.Report       `json:"legacy_opening"`
	ATR     s01.ATRReport           `json:"legacy_atr"`
	Noise   backtestintraday.Report `json:"noise_price_replay"`
	Gap     backtestintraday.Report `json:"gap_price_replay"`
	Models  []researchbook.Case     `json:"models"`
	Cohorts map[string]bool         `json:"comparison_cohorts"`
}
type round3Matrix struct {
	Version string            `json:"version"`
	Symbol  string            `json:"symbol"`
	Source  marketdata.Source `json:"minute_source"`
	Rows    []round3MatrixRow `json:"rows"`
	Holdout bool              `json:"holdout_pnl_read"`
	Quality bool              `json:"quality_promoted"`
}
type round3StudyReport struct {
	Version       string          `json:"version"`
	Phase         string          `json:"phase"`
	ManifestSHA   string          `json:"manifest_sha256"`
	ScopeSHA      string          `json:"strategy_plan_sha256"`
	PeriodSHA     string          `json:"period_sha256"`
	NormalizedSHA string          `json:"normalized_index_sha256"`
	Sessions      []string        `json:"evaluated_sessions"`
	Matrices      []studyArtifact `json:"case_matrices"`
	Books         []studyBook     `json:"books"`
	HoldoutRead   bool            `json:"holdout_pnl_read"`
	NewPNL        bool            `json:"new_pnl_evaluation"`
	Adopted       bool            `json:"strategy_adopted"`
	Trading       bool            `json:"trading_eligible"`
	Limitations   []string        `json:"limitations"`
	FreezeSHA     string          `json:"final_freeze_sha256,omitempty"`
}

func round3Cohorts(rows []researchbook.Case) (map[string]bool, error) {
	if len(rows) != 3 {
		return nil, errors.New("旧S01/新2候補の比較列を確認してください")
	}
	c := map[string]bool{"new_two": rows[1].Status != "unevaluable" && rows[2].Status != "unevaluable"}
	c["all_three"] = c["new_two"] && rows[0].Status != "unevaluable"
	for i, id := range round3Models[1:] {
		if rows[i+1].Date != rows[0].Date || rows[i+1].Symbol != rows[0].Symbol || rows[i+1].Rank != rows[0].Rank {
			return nil, errors.New("第3回の比較日/銘柄/順位が一致しません")
		}
		c["pair_"+id] = rows[0].Status != "unevaluable" && rows[i+1].Status != "unevaluable"
	}
	return c, nil
}

func round3Gate(report researchbook.Report) (round3GateReport, error) {
	g := round3GateReport{ScopeSHA: round3ScopeSHA, Decision: report.Decision, Reasons: append([]string{}, report.GateReasons...)}
	dd, ok := s01.Exact(report.Drawdown)
	if !ok {
		return g, errors.New("第3回の実現ドローダウンを確認してください")
	}
	if report.Cases == 0 || (report.Cases-report.Unevaluable)*5 < report.Cases*4 {
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

func round3Case(date, symbol string, rank int, r backtestintraday.Report) researchbook.Case {
	c := researchbook.Case{Date: date, Symbol: symbol, Rank: rank, Status: r.Status, Reasons: append([]string{}, r.Reasons...)}
	if c.Status == "traded" {
		c.Status = "trade"
	}
	if r.Trade != nil {
		t := r.Trade
		c.Trade = &researchbook.Trade{Direction: t.Direction, EntryAt: t.EntryAt, ExitAt: t.ExitAt, EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice, Reason: t.Reason}
	}
	return c
}

func runRound3Study(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("study-round3", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "条件/期間/母集団/分足/独立入力検査のhashを固定したmanifest")
	phase := fs.String("phase", "validation", "validation / holdout。最終は同版のfreeze必須")
	freeze := fs.String("freeze", "", "固定コード/検証結果/独立検算/全体検証を固定した最終開封JSON")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if *input == "" || fs.NArg() != 0 || *phase != "validation" && *phase != "holdout" || *phase == "holdout" && *freeze == "" || *phase == "validation" && *freeze != "" {
		return errors.New("第3回の最終評価は検証/独立検算/同一版の固定後だけ開けます")
	}
	raw, e := readInput(*input)
	if e != nil {
		return e
	}
	freezeSHA := ""
	if *phase == "holdout" {
		freezeSHA, e = checkRound3Freeze(*freeze, raw)
		if e != nil {
			return e
		}
		revision, _ := studyBuildIdentity()
		if e := reserveStudyOpeningAt("data/research/round3-holdout-opening-20261019.json", freezeSHA, hash(raw), revision); e != nil {
			return e
		}
	}
	in, e := loadRound3Inputs(ctx, *input, raw, *phase)
	if e != nil {
		return e
	}
	report := round3StudyReport{Version: round3Version, Phase: *phase, ManifestSHA: hash(raw), ScopeSHA: round3ScopeSHA, PeriodSHA: round3PeriodSHA, NormalizedSHA: in.Manifest.Normalized.SHA, Sessions: append([]string{}, in.Part.Dates...), Matrices: []studyArtifact{}, Books: []studyBook{}, HoldoutRead: *phase == "holdout", NewPNL: true, FreezeSHA: freezeSHA, Limitations: []string{"fixed_two_hypotheses_no_combination_or_parameter_grid", "retrospective_partitions_previously_used_for_indicator_warmup_not_pristine_unseen_or_forward", "later_known_exploration_informed_hypothesis_selection_not_chronological_walk_forward", "same_provider_ex_post_quality_and_minute_open_model_not_actual_fills", "ordinary_margin_400000_jpy_100_shares_one_position_not_live_limits", "all_24_cost_sensitivities_preserved_primary_10bps_not_tuned_from_quotes", "five_evaluated_months_35750_jpy_not_continuous_annual_subscription_bill", "native_all_and_common_all_three_and_pairs_kept_separately", "no_borrow_inventory_or_realized_slippage_proof"}}
	if !report.HoldoutRead {
		report.Limitations = append(report.Limitations, "validation_only_holdout_pnl_not_computed")
	}
	cases := map[string][]researchbook.Case{}
	cohorts := map[string]map[string]bool{}
	checked := map[string]string{}
	symbols := []string{}
	for symbol := range in.Files {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	for si, symbol := range symbols {
		joined, e := round3Joined(ctx, in.Files[symbol], checked)
		if e != nil {
			return e
		}
		days := map[string]marketdata.Day{}
		for _, day := range joined.Days {
			days[day.Date] = day
		}
		daily := []marketdata.DailyBar{}
		code := strings.TrimSuffix(symbol, "@1") + "0"
		for _, d := range in.Daily {
			if d.Code == code {
				daily = append(daily, d)
			}
		}
		sort.Slice(daily, func(i, j int) bool { return daily[i].Date < daily[j].Date })
		matrix := round3Matrix{Version: round3Version, Symbol: symbol, Source: joined.Source, Rows: []round3MatrixRow{}, Holdout: report.HoldoutRead}
		for _, day := range in.Universe.Universe.Days {
			rank := 0
			for i, pick := range day.Picks {
				if pick.Code == code {
					rank = i + 1
					break
				}
			}
			if rank == 0 {
				continue
			}
			at := sort.SearchStrings(in.Sessions, day.Date)
			if at < 15 || at >= len(in.Sessions) || in.Sessions[at] != day.Date {
				return errors.New("第3回の公式参照14/15日が不足しています")
			}
			previous := in.Sessions[at-14 : at]
			wanted := map[string]bool{day.Date: true}
			for _, date := range previous {
				wanted[date] = true
			}
			subset := joined
			subset.Days = []marketdata.Day{}
			openingSubset := joined
			openingSubset.Days = []marketdata.Day{}
			for _, d := range joined.Days {
				if !wanted[d.Date] {
					continue
				}
				subset.Days = append(subset.Days, d)
				o := d
				o.Bars = []marketdata.Bar{}
				o.Events = nil
				for _, b := range d.Bars {
					if !b.Start.Before(marketdata.Clock(d.Date, 9, 0)) && !b.Start.After(marketdata.Clock(d.Date, 9, 4)) {
						o.Bars = append(o.Bars, b)
					}
				}
				openingSubset.Days = append(openingSubset.Days, o)
			}
			if len(subset.Days) == 0 {
				return errors.New("選択銘柄の全参照分足がありません")
			}
			refs := []marketdata.DailyBar{}
			for _, d := range daily {
				if wanted[d.Date] {
					refs = append(refs, d)
				}
			}
			opening, e := prepareorb.CheckTurnoverJQuants(ctx, openingSubset, day.Date, previous)
			if e != nil {
				return e
			}
			atr, e := s01.ATR(ctx, daily, symbol, day.Date, in.Sessions[at-15:at])
			if e != nil {
				return e
			}
			current := in.Daily[code+"/"+day.Date]
			legacy := researchbook.Case{Date: day.Date, Symbol: symbol, Rank: rank, Status: "unevaluable", Reasons: []string{}}
			if !opening.CalculationAvailable {
				legacy.Reasons = append(legacy.Reasons, "opening_rvol_unavailable")
			}
			if !atr.Available || atr.StopDistance == nil || *atr.StopDistance <= 0 {
				legacy.Reasons = append(legacy.Reasons, "atr_or_positive_stop_unavailable")
			}
			target := joined
			if d, exists := days[day.Date]; exists {
				target.Days = []marketdata.Day{d}
			}
			if len(legacy.Reasons) == 0 {
				o := opening.Target.Range
				result, e := backtests01.DiagnoseTurnoverJQuants(ctx, target, day.Date, current, s01.Range{Open: o.Open, High: o.High, Low: o.Low, Close: o.Close}, *atr.StopDistance, *opening.RelativeVolume)
				if e != nil {
					return e
				}
				legacy.Status, legacy.Reasons = result.Status, result.Reasons
				if legacy.Status == "traded" {
					legacy.Status = "trade"
				}
				if result.Trade != nil {
					t := result.Trade
					legacy.Trade = &researchbook.Trade{Direction: string(t.Direction), EntryAt: t.EntryAt, ExitAt: t.ExitAt, EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice, Reason: t.Reason}
				}
			}
			noise, e := backtestintraday.Diagnose(ctx, subset, day.Date, previous, refs, round3.NoiseRule)
			if e != nil {
				return e
			}
			gap, e := backtestintraday.Diagnose(ctx, subset, day.Date, previous, refs, round3.GapRule)
			if e != nil {
				return e
			}
			rows := []researchbook.Case{legacy, round3Case(day.Date, symbol, rank, noise), round3Case(day.Date, symbol, rank, gap)}
			common, e := round3Cohorts(rows)
			if e != nil {
				return e
			}
			cohorts[day.Date+"/"+symbol] = common
			for i := range rows {
				rows[i].Common = common["new_two"]
				if i == 0 {
					rows[i].Common = common["all_three"]
				}
				cases[round3Models[i]] = append(cases[round3Models[i]], rows[i])
			}
			matrix.Rows = append(matrix.Rows, round3MatrixRow{Date: day.Date, Rank: rank, Opening: opening, ATR: atr, Noise: noise, Gap: gap, Models: rows, Cohorts: common})
		}
		path, body, e := saveContentAddressed("round3-cases", matrix)
		if e != nil {
			return e
		}
		report.Matrices = append(report.Matrices, studyArtifact{path, hash(body)})
		if (si+1)%5 == 0 || si+1 == len(symbols) {
			if _, e := fmt.Fprintf(out, "第3回価格行列: phase=%s / symbols=%d/%d / holdout_pnl_read=%t\n", *phase, si+1, len(symbols), report.HoldoutRead); e != nil {
				return e
			}
		}
	}
	for _, model := range round3Models {
		rows := cases[model]
		if len(rows) != in.Part.Sessions*30 {
			return errors.New("第3回の全日付30枠の行列を確認してください")
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Date+"/"+rows[i].Symbol < rows[j].Date+"/"+rows[j].Symbol })
		names := []string{"native", "all_three"}
		if model == s01.Version {
			names = append(names, "pair_"+round3.NoiseRule, "pair_"+round3.GapRule)
		} else {
			names = append(names, "pair_"+model)
		}
		for _, name := range names {
			selected := append([]researchbook.Case{}, rows...)
			if name != "native" {
				for i := range selected {
					selected[i].Common = cohorts[selected[i].Date+"/"+selected[i].Symbol][name]
				}
			}
			for _, common := range []bool{false, true} {
				if name != "native" && !common {
					continue
				}
				for _, bps := range []int64{0, 5, 10, 20} {
					for _, commission := range []string{"sor_conditional_zero", "non_sor_standard"} {
						for _, extra := range []int64{0, 100, 500} {
							scenario := researchbook.Scenario{CommonOnly: common, FrictionBPS: bps, Commission: commission, ShortExtra: extra}
							var book researchbook.Report
							var e error
							if model == s01.Version {
								book, e = researchbook.Run(ctx, selected, in.FeeCalendar, in.Part.Dates, scenario)
							} else {
								clock := researchbook.Morning30
								if model == round3.GapRule {
									clock = researchbook.Morning29
								}
								book, e = researchbook.RunTimedMorning(ctx, selected, in.FeeCalendar, in.Part.Dates, scenario, clock)
							}
							if e != nil {
								return e
							}
							path, body, e := saveContentAddressed("round3-book", book)
							if e != nil {
								return e
							}
							book.Ledger = nil
							item := studyBook{Model: model, Partition: *phase, Artifact: studyArtifact{path, hash(body)}, Summary: book, Cohort: name}
							if name == "native" && model != s01.Version {
								gate, e := round3Gate(book)
								if e != nil {
									return e
								}
								item.Round3Gate = &gate
							}
							report.Books = append(report.Books, item)
						}
					}
				}
			}
		}
	}
	if len(report.Books) != round3BookCount {
		return errors.New("第3回の13比較枠×24費用条件が不足しています")
	}
	path, body, e := save("round3-study", report)
	if e != nil {
		return e
	}
	record, e := recordRun("study-round3", filepath.Base(*input), raw, hash(raw), path, body, struct{ Phase, FreezeSHA string }{*phase, freezeSHA})
	if e != nil {
		return e
	}
	if _, e := fmt.Fprintf(out, "第3回研究: phase=%s / cases=%d / books=%d / holdout_pnl_read=%t / quality_promoted=false\n保存: %s\n実行記録: %s\n", *phase, in.Part.Sessions*30, len(report.Books), report.HoldoutRead, path, record); e != nil {
		return e
	}
	for _, book := range report.Books {
		if book.Cohort == "native" && !book.Summary.Scenario.CommonOnly && book.Summary.Scenario.FrictionBPS == 10 && book.Summary.Scenario.Commission == "sor_conditional_zero" && book.Summary.Scenario.ShortExtra == 0 {
			decision := book.Summary.Decision
			if book.Round3Gate != nil {
				decision = book.Round3Gate.Decision
			}
			if _, e := fmt.Fprintf(out, "主要条件: model=%s / executed=%d / net_after_data_jpy=%s / gate=%s\n", book.Model, book.Summary.Executed, formatStudyMoney(book.Summary.AfterData), decision); e != nil {
				return e
			}
		}
	}
	return nil
}
