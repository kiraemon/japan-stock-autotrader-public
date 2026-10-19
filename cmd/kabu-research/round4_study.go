package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/researchbook"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/round3"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/round4"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

const round4SourceSHA = "5c8a5be069ecc657cd97cd25f7239c8ad6af592f768e131e1540d917408e02ba"

var round4RootSHA = map[string]string{"validation": "ba1aa3afc883ddf5a3e54396ec4933b39590207d066427eeef08596c5b02629c", "holdout": "bb3b342314df929387188a12df5fac2e03be0808f16e928b6db4e792011be662"}
var round4Models = []string{s01.Version, round4.SelectionRule, round3.NoiseRule, round4.ExitRule}

type round4Manifest struct {
	Version int                      `json:"version"`
	Plan    studyArtifact            `json:"plan"`
	Source  studyArtifact            `json:"source_manifest"`
	Roots   map[string]studyArtifact `json:"opened_round3_roots"`
}
type round4Row struct {
	Date   string              `json:"date_jst"`
	Symbol string              `json:"symbol"`
	Rank   int                 `json:"liquidity_rank"`
	Pick   round4.Pick         `json:"opening_selection"`
	Exit   *round4.Exit        `json:"changed_exit,omitempty"`
	Models []researchbook.Case `json:"models"`
}
type round4Report struct {
	Version       string          `json:"version"`
	Phase         string          `json:"exploration_phase"`
	ManifestSHA   string          `json:"manifest_sha256"`
	PlanSHA       string          `json:"strategy_plan_sha256"`
	SourceRootSHA string          `json:"opened_source_root_sha256"`
	Sessions      []string        `json:"evaluated_sessions"`
	Matrices      []studyArtifact `json:"case_matrices"`
	Books         []studyBook     `json:"books"`
	Adopted       bool            `json:"strategy_adopted"`
	Trading       bool            `json:"trading_eligible"`
	Unseen        bool            `json:"unseen_evaluation"`
	Decision      string          `json:"adoption_decision"`
	Limitations   []string        `json:"limitations"`
}

func validateRound4Manifest(m round4Manifest) error {
	if m.Version != 1 || m.Plan.SHA != round4.PlanSHA || m.Source.SHA != round4SourceSHA || len(m.Roots) != 2 {
		return errors.New("第4回の事前固定計画/開封済み入力を確認してください")
	}
	for phase, want := range round4RootSHA {
		if m.Roots[phase].SHA != want {
			return errors.New("第4回の開封済み参照rootが一致しません")
		}
	}
	return nil
}

func selectRound4Day(ctx context.Context, rows []round3MatrixRow) ([]round4.Pick, error) {
	input := []round4.Opening{}
	for _, r := range rows {
		if len(r.Models) != 3 {
			return nil, errors.New("第4回の参照列が不足しています")
		}
		o := round4.Opening{Symbol: r.Models[0].Symbol, LiquidityRank: r.Rank, Available: r.Opening.CalculationAvailable}
		if o.Available {
			if r.Opening.RelativeVolume == nil {
				return nil, errors.New("第4回のRVOLがありません")
			}
			o.RVOL = *r.Opening.RelativeVolume
		}
		input = append(input, o)
	}
	return round4.Select(ctx, input)
}

func copyRound4Case(c researchbook.Case) researchbook.Case {
	c.Reasons = append([]string{}, c.Reasons...)
	c.Common = false
	if c.Trade != nil {
		v := *c.Trade
		c.Trade = &v
	}
	return c
}

func runRound4Study(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("study-round4", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "固定計画/開封済みroot/既存入力のhashを固定したmanifest")
	phase := fs.String("phase", "early", "early / late。どちらも損益既知期間の探索")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if *input == "" || fs.NArg() != 0 || *phase != "early" && *phase != "late" {
		return errors.New("第4回は固定したearly/late探索だけに限定しています")
	}
	raw, e := readInput(*input)
	if e != nil {
		return e
	}
	var m round4Manifest
	if strictJSON(raw, &m) != nil {
		return errors.New("第4回manifest形式を確認してください")
	}
	if e = validateRound4Manifest(m); e != nil {
		return e
	}
	if _, e = pinnedInput(relativeORBPath(*input, m.Plan.Path), m.Plan.SHA); e != nil {
		return e
	}
	sourcePath := relativeORBPath(*input, m.Source.Path)
	sourceRaw, e := pinnedInput(sourcePath, m.Source.SHA)
	if e != nil {
		return e
	}
	oldPhase := "validation"
	if *phase == "late" {
		oldPhase = "holdout"
	}
	rootPin := m.Roots[oldPhase]
	rootRaw, e := pinnedInput(relativeORBPath(*input, rootPin.Path), rootPin.SHA)
	if e != nil {
		return e
	}
	var root round3StudyReport
	if strictJSON(rootRaw, &root) != nil || root.Version != round3Version || root.Phase != oldPhase || root.ManifestSHA != m.Source.SHA {
		return errors.New("第4回の固定参照rootを確認してください")
	}
	in, e := loadRound3Inputs(ctx, sourcePath, sourceRaw, oldPhase)
	if e != nil {
		return e
	}
	byDate := map[string][]round3MatrixRow{}
	seen := map[string]bool{}
	for _, p := range root.Matrices {
		body, e := pinnedInput(p.Path, p.SHA)
		if e != nil {
			return e
		}
		var matrix round3Matrix
		if strictJSON(body, &matrix) != nil || matrix.Version != round3Version {
			return errors.New("第4回の参照価格行列を確認してください")
		}
		for _, row := range matrix.Rows {
			key := row.Date + "/" + matrix.Symbol
			if seen[key] || len(row.Models) != 3 || row.Models[0].Symbol != matrix.Symbol || row.Models[0].Date != row.Date || row.Models[0].Rank != row.Rank {
				return errors.New("参照価格行列の対応が不正です")
			}
			seen[key] = true
			byDate[row.Date] = append(byDate[row.Date], row)
		}
	}
	if len(byDate) != in.Part.Sessions || len(seen) != in.Part.Sessions*30 {
		return errors.New("第4回の対象日/30枠が不足しています")
	}
	picks := map[string]round4.Pick{}
	for _, date := range in.Part.Dates {
		selected, e := selectRound4Day(ctx, byDate[date])
		if e != nil {
			return e
		}
		for _, p := range selected {
			picks[date+"/"+p.Symbol] = p
		}
	}
	cases := map[string][]researchbook.Case{}
	result := round4Report{Version: "round4-two-improvements-exploration-v0.1", Phase: *phase, ManifestSHA: hash(raw), PlanSHA: round4.PlanSHA, SourceRootSHA: rootPin.SHA, Sessions: in.Part.Dates, Matrices: []studyArtifact{}, Books: []studyBook{}, Decision: "hold_insufficient_unseen_validation_and_final", Limitations: []string{"both_partitions_known_pnl_not_new_unseen_validation_or_holdout", "two_fixed_changes_no_grid_or_combination", "audited_pinned_original_indicators_and_price_cases_reused_not_new_source_audit", "RVOL_ranking_only_within_liquidity_top30_unavailable_openings_excluded_not_zero", "full_original_quality_window_retained_even_after_early_exit", "completed_minute_then_next_open_is_price_model_not_actual_fill", "ordinary_margin_400000_100shares_one_position_not_live_approval", "borrow_inventory_short_extra_and_SOR_actual_eligibility_unverified", "five_evaluated_months_subscription_not_continuous_annual_bill"}}
	symbols := []string{}
	for symbol := range in.Files {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	checked := map[string]string{}
	for si, symbol := range symbols {
		if ctx.Err() != nil {
			return errors.New("第4回研究を中断しました")
		}
		joined, e := round3Joined(ctx, in.Files[symbol], checked)
		if e != nil {
			return e
		}
		days := map[string]marketdata.Day{}
		for _, d := range joined.Days {
			days[d.Date] = d
		}
		rows := []round4Row{}
		for _, date := range in.Part.Dates {
			var old *round3MatrixRow
			for i := range byDate[date] {
				if byDate[date][i].Models[0].Symbol == symbol {
					old = &byDate[date][i]
					break
				}
			}
			if old == nil {
				continue
			}
			a, b := copyRound4Case(old.Models[0]), copyRound4Case(old.Models[1])
			filtered, exited := copyRound4Case(a), copyRound4Case(b)
			pick := picks[date+"/"+symbol]
			if filtered.Status != "unevaluable" && !pick.Selected {
				filtered.Status = "no_trade"
				filtered.Trade = nil
				filtered.Reasons = append(filtered.Reasons, "outside_fixed_RVOL_top20")
			}
			var changed *round4.Exit
			if exited.Status == "trade" {
				day, ok := days[date]
				if !ok {
					return errors.New("VWAP退出の対象日がありません")
				}
				target := joined
				target.Days = []marketdata.Day{day}
				current := in.Daily[symbol[:4]+"0/"+date]
				window, e := marketdata.CheckJQuantsTurnoverWindow(ctx, target, date, current)
				if e != nil {
					return e
				}
				if !window.Eligible {
					return errors.New("固定参照の品質窓が一致しません")
				}
				entryFound := false
				for _, bar := range day.Bars {
					if bar.Start.Equal(exited.Trade.EntryAt) {
						entryFound = bar.Open == exited.Trade.EntryPrice
					}
				}
				if !entryFound {
					return errors.New("固定参照の参入価格が一致しません")
				}
				x, e := round4.VWAPExit(ctx, day, exited.Trade.Direction)
				if e != nil {
					return e
				}
				changed = &x
				exited.Trade.ExitAt = x.At
				exited.Trade.ExitPrice = x.Price
				exited.Trade.Reason = x.Reason
			}
			models := []researchbook.Case{a, filtered, b, exited}
			for pair := 0; pair < 2; pair++ {
				j := pair * 2
				common := models[j].Status != "unevaluable" && models[j+1].Status != "unevaluable"
				models[j].Common = common
				models[j+1].Common = common
			}
			for i, c := range models {
				cases[round4Models[i]] = append(cases[round4Models[i]], c)
			}
			rows = append(rows, round4Row{Date: date, Symbol: symbol, Rank: old.Rank, Pick: pick, Exit: changed, Models: models})
		}
		p, body, e := saveContentAddressed("round4-cases", rows)
		if e != nil {
			return e
		}
		result.Matrices = append(result.Matrices, studyArtifact{p, hash(body)})
		if (si+1)%10 == 0 || si+1 == len(symbols) {
			if _, e = fmt.Fprintf(out, "第4回探索行列: phase=%s / symbols=%d/%d / unseen_evaluation=false\n", *phase, si+1, len(symbols)); e != nil {
				return e
			}
		}
	}
	for _, model := range round4Models {
		rows := cases[model]
		if len(rows) != in.Part.Sessions*30 {
			return errors.New("第4回の比較ケースが不足しています")
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Date+"/"+rows[i].Symbol < rows[j].Date+"/"+rows[j].Symbol })
		for _, common := range []bool{false, true} {
			for _, bps := range []int64{0, 5, 10, 20} {
				for _, commission := range []string{"sor_conditional_zero", "non_sor_standard"} {
					for _, extra := range []int64{0, 100, 500} {
						scenario := researchbook.Scenario{CommonOnly: common, FrictionBPS: bps, Commission: commission, ShortExtra: extra}
						var book researchbook.Report
						if model == s01.Version || model == round4.SelectionRule {
							book, e = researchbook.Run(ctx, rows, in.FeeCalendar, in.Part.Dates, scenario)
						} else {
							clock := researchbook.Morning30
							if model == round4.ExitRule {
								clock = researchbook.Morning30VWAP
							}
							book, e = researchbook.RunTimedMorning(ctx, rows, in.FeeCalendar, in.Part.Dates, scenario, clock)
						}
						if e != nil {
							return e
						}
						p, body, e := saveContentAddressed("round4-book", book)
						if e != nil {
							return e
						}
						book.Ledger = nil
						cohort := "native"
						if common {
							cohort = "pair_common"
						}
						result.Books = append(result.Books, studyBook{Model: model, Partition: *phase, Artifact: studyArtifact{p, hash(body)}, Summary: book, Cohort: cohort})
					}
				}
			}
		}
	}
	if len(result.Books) != 192 {
		return errors.New("第4回192ブックが不足しています")
	}
	p, body, e := save("round4-study", result)
	if e != nil {
		return e
	}
	record, e := recordRun("study-round4", filepath.Base(*input), raw, hash(raw), p, body, struct{ Phase string }{*phase})
	if e != nil {
		return e
	}
	if _, e = fmt.Fprintf(out, "第4回探索: phase=%s / cases=%d / books=%d / unseen_evaluation=false / strategy_adopted=false\n保存: %s\n実行記録: %s\n", *phase, len(seen), len(result.Books), p, record); e != nil {
		return e
	}
	for _, b := range result.Books {
		sc := b.Summary.Scenario
		if b.Cohort == "native" && sc.FrictionBPS == 10 && sc.Commission == "sor_conditional_zero" && sc.ShortExtra == 0 {
			if _, e = fmt.Fprintf(out, "探索主要条件: model=%s / executed=%d / after_data_jpy=%s / adoption=hold_insufficient_unseen\n", b.Model, b.Summary.Executed, formatStudyMoney(b.Summary.AfterData)); e != nil {
				return e
			}
		}
	}
	return nil
}
