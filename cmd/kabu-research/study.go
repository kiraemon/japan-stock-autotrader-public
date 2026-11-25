package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/backtest"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/backtests01"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/researchbook"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/s01"
)

type studyManifest struct {
	Version        int    `json:"version"`
	Preparation    string `json:"preparation_manifest"`
	PreparationSHA string `json:"preparation_sha256"`
	Indicators     string `json:"indicator_index"`
	IndicatorsSHA  string `json:"indicator_index_sha256"`
	Period         string `json:"period_plan"`
	PeriodSHA      string `json:"period_sha256"`
}
type studyPartition struct {
	Sessions int    `json:"sessions"`
	From     string `json:"from"`
	To       string `json:"to"`
}

var fixedPartitions = map[string]studyPartition{"train": {108, "2026-01-05", "2026-06-15"}, "validation": {36, "2026-06-16", "2026-08-05"}, "holdout": {36, "2026-08-06", "2026-09-30"}}
var studyModels = []string{s01.Version, "R001-A-long", "R001-A-short", "R001-B-long", "R001-B-short"}

func formatStudyMoney(value string) string {
	r, ok := new(big.Rat).SetString(value)
	if !ok {
		return "unavailable"
	}
	return r.FloatString(2)
}

type studyMatrixRow struct {
	Date         string                   `json:"date_jst"`
	Rank         int                      `json:"liquidity_rank"`
	Window       marketdata.JQuantsWindow `json:"price_window"`
	Models       []researchbook.Case      `json:"models"`
	PriorActions []string                 `json:"raw_volume_reference_action_dates"`
	Cohorts      *round2Cohorts           `json:"round2_comparison_cohorts,omitempty"`
}
type studyMatrix struct {
	Version     string           `json:"version"`
	Symbol      string           `json:"symbol"`
	InputSHA    string           `json:"indicator_file_sha256"`
	Models      []string         `json:"model_columns"`
	Rows        []studyMatrixRow `json:"rows"`
	HoldoutRead bool             `json:"holdout_pnl_read"`
	Quality     bool             `json:"quality_promoted"`
	NewPNL      bool             `json:"new_pnl_evaluation"`
}
type studyArtifact struct {
	Path string `json:"path"`
	SHA  string `json:"sha256"`
}
type studyBook struct {
	Model      string              `json:"model"`
	Partition  string              `json:"partition"`
	Artifact   studyArtifact       `json:"artifact"`
	Summary    researchbook.Report `json:"summary"`
	Cohort     string              `json:"comparison_cohort,omitempty"`
	Round2Gate *round2GateReport   `json:"round2_candidate_gate,omitempty"`
	Round3Gate *round3GateReport   `json:"round3_candidate_gate,omitempty"`
}
type studyReport struct {
	Version       string          `json:"version"`
	Phase         string          `json:"phase"`
	ManifestSHA   string          `json:"manifest_sha256"`
	PlanSHA       string          `json:"strategy_plan_sha256"`
	IndicatorsSHA string          `json:"indicator_index_sha256"`
	PeriodSHA     string          `json:"period_sha256"`
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

func studyPeriods(raw []byte, sessions []string) (map[string][]string, error) {
	var p struct {
		Version    int                       `json:"version"`
		Fixed      bool                      `json:"fixed_before_new_pnl"`
		Sessions   int                       `json:"calendar_sessions"`
		Partitions map[string]studyPartition `json:"partitions"`
		Adopted    bool                      `json:"strategy_adopted"`
		Orders     bool                      `json:"real_orders_enabled"`
	}
	fail := func() (map[string][]string, error) {
		return nil, errors.New("固定した108/36/36営業日の期間計画を確認してください")
	}
	if json.Unmarshal(raw, &p) != nil || p.Version != 1 || !p.Fixed || p.Sessions != 180 || len(sessions) != 180 || len(p.Partitions) != 3 || p.Adopted || p.Orders {
		return fail()
	}
	dates := map[string][]string{}
	for name, want := range fixedPartitions {
		if p.Partitions[name] != want {
			return fail()
		}
		for _, date := range sessions {
			if date >= want.From && date <= want.To {
				dates[name] = append(dates[name], date)
			}
		}
		if len(dates[name]) != want.Sessions || dates[name][0] != want.From || dates[name][len(dates[name])-1] != want.To {
			return fail()
		}
	}
	return dates, nil
}

func runStudy(ctx context.Context, args []string, out io.Writer) error {
	return runStudyMode(ctx, args, out, false)
}

func runStudyMode(ctx context.Context, args []string, out io.Writer, round2 bool) error {
	command, planSHA, prepVersion := "study-s01", s01.PlanSHA256, 1
	indexVersion, stockVersion := "s01-input-index-v0.1", "s01-stock-input-v0.1"
	reportVersion, reportPrefix, matrixPrefix, bookPrefix := "s01-comparative-study-v0.1", "s01-study", "study-cases", "study-book"
	models := append([]string{}, studyModels...)
	if round2 {
		command, planSHA, prepVersion = "study-s01-round2", s01.Round2ScopeSHA256, 2
		indexVersion, stockVersion = "s01-round2-input-index-v0.1", "s01-round2-stock-input-v0.1"
		reportVersion, reportPrefix, matrixPrefix, bookPrefix = "s01-round2-study-v0.1", "s01-r2-study", "s01-r2-cases", "s01-r2-book"
		models = append(models, "R2-P0", "R2-P1", "R2-P2")
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "指標索引/入力manifest/期間計画をhash固定した研究manifest")
	phase := fs.String("phase", "development", "development / holdout。holdoutは固定したfreeze必須")
	freeze := fs.String("freeze", "", "最終コード/開発結果/検証/同一計算ソースを固定したJSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if (*phase != "development" && *phase != "holdout") || (*phase == "holdout" && *freeze == "") || (*phase == "development" && *freeze != "") {
		return errors.New("最終評価は開発/検証後にコード/費用/判定を固定するまで開きません")
	}
	if fs.NArg() != 0 || *input == "" {
		return errors.New("study-s01には固定したinput manifestを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	var m studyManifest
	if strictJSON(raw, &m) != nil || m.Version != 1 {
		return errors.New("一括研究manifestの版/形式を確認してください")
	}
	resolve := func(p string) string { return relativeORBPath(*input, p) }
	prepRaw, err := pinnedInput(resolve(m.Preparation), m.PreparationSHA)
	if err != nil {
		return err
	}
	var prep s01InputManifest
	if strictJSON(prepRaw, &prep) != nil || prep.Version != prepVersion || prep.PlanSHA != planSHA {
		return errors.New("固定したS01準備条件を確認してください")
	}
	prepPath := resolve(m.Preparation)
	prepResolve := func(p string) string { return relativeORBPath(prepPath, p) }
	if _, err := pinnedInput(prepResolve(prep.Plan), prep.PlanSHA); err != nil {
		return err
	}
	iRaw, err := pinnedInput(resolve(m.Indicators), m.IndicatorsSHA)
	if err != nil {
		return err
	}
	var idx s01InputIndex
	if strictJSON(iRaw, &idx) != nil || idx.Version != indexVersion || idx.PlanSHA != prep.PlanSHA || idx.ManifestSHA != m.PreparationSHA || idx.UniverseSHA != prep.UniverseSHA || idx.NormalizationSHA != prep.NormalizationSHA || idx.ReconciliationSHA != prep.ReconciliationSHA || idx.Quality || idx.NewPNL || idx.TradingEligible {
		return errors.New("準備条件と一致する未昇格の指標索引を確認してください")
	}
	uRaw, err := pinnedInput(prepResolve(prep.Universe), prep.UniverseSHA)
	if err != nil {
		return err
	}
	var u universeOutput
	if strictJSON(uRaw, &u) != nil || u.Version != "s01-universe-v0.1" || u.Universe.Version != u.Version || u.Universe.QualityPromoted || u.Universe.NewPNL || u.Universe.StrategyAdopted || u.Universe.TradingEligible || u.DailySHA != idx.DailySHA {
		return errors.New("固定した未昇格の母集団を確認してください")
	}
	nRaw, err := pinnedInput(prepResolve(prep.Normalization), prep.NormalizationSHA)
	if err != nil {
		return err
	}
	var normalized s01Normalization
	if strictJSON(nRaw, &normalized) != nil || !normalized.Complete || normalized.Quality || normalized.NewPNL || normalized.UniverseSHA != idx.UniverseSHA {
		return errors.New("完了した未昇格の正規化索引を確認してください")
	}
	if _, err := pinnedInput(prepResolve(prep.Reconciliation), prep.ReconciliationSHA); err != nil {
		return err
	}
	normalizedByKey := map[string]s01MinuteFile{}
	for _, f := range normalized.Records {
		key := f.Symbol + "/" + f.Month
		if _, ok := normalizedByKey[key]; ok {
			return errors.New("元の分足索引が重複しています")
		}
		normalizedByKey[key] = f
	}
	dRaw, err := pinnedInput(u.DailyCatalog, u.DailySHA)
	if err != nil {
		return err
	}
	var catalog universeDailyCatalog
	if strictJSON(dRaw, &catalog) != nil || catalog.Version != "jquants-unadjusted-daily-v0.1" || catalog.QualityPromoted || catalog.Origin != "observed" || catalog.PriceScale != marketdata.PriceScale {
		return errors.New("未調整の日足カタログを確認してください")
	}
	daily := map[string]marketdata.DailyBar{}
	for _, d := range catalog.Bars {
		key := d.Code + "/" + d.Date
		if _, ok := daily[key]; ok {
			return errors.New("日足が重複しています")
		}
		daily[key] = d
	}
	sessions := []string{}
	expected := map[string]int{}
	for _, day := range u.Universe.Days {
		if len(day.Picks) != 30 || len(day.Problems) > 0 {
			return errors.New("日別30銘柄の固定母集団を確認してください")
		}
		sessions = append(sessions, day.Date)
		for rank, pick := range day.Picks {
			if len(pick.Code) != 5 || !strings.HasSuffix(pick.Code, "0") {
				return errors.New("普通株のコードを確認してください")
			}
			key := day.Date + "/" + pick.Code[:4] + "@1"
			if _, ok := expected[key]; ok {
				return errors.New("母集団が重複しています")
			}
			expected[key] = rank + 1
		}
	}
	pRaw, err := pinnedInput(resolve(m.Period), m.PeriodSHA)
	if err != nil {
		return err
	}
	periods := studyPeriods
	if round2 {
		if prep.PeriodSHA != m.PeriodSHA {
			return errors.New("指標準備と比較の固定期間が一致しません")
		}
		if _, err := pinnedInput(prepResolve(prep.Period), m.PeriodSHA); err != nil {
			return err
		}
		periods = round2Periods
	}
	parts, err := periods(pRaw, sessions)
	if err != nil {
		return err
	}
	if idx.Cases != len(sessions)*30 || len(expected) != len(sessions)*30 || len(idx.Files) != len(u.Universe.Union) {
		return errors.New("全日付/銘柄の指標行列を確認してください")
	}
	development := append(append([]string{}, parts["train"]...), parts["validation"]...)
	enabledParts := []string{"train", "validation"}
	displayPart := "validation"
	freezeSHA := ""
	if *phase == "holdout" {
		if round2 {
			freezeSHA, err = checkRound2Freeze(*freeze, raw)
		} else {
			freezeSHA, err = checkStudyFreeze(*freeze, raw)
		}
		if err != nil {
			return err
		}
		revision, _ := studyBuildIdentity()
		if err := reserveStudyOpeningMode(freezeSHA, hash(raw), revision, round2); err != nil {
			return err
		}
		development = append([]string{}, parts["holdout"]...)
		enabledParts = []string{"holdout"}
		displayPart = "holdout"
	}
	allowed := map[string]bool{}
	for _, date := range development {
		allowed[date] = true
	}
	r := studyReport{Version: "s01-comparative-study-v0.1", Phase: *phase, ManifestSHA: hash(raw), PlanSHA: prep.PlanSHA, IndicatorsSHA: m.IndicatorsSHA, PeriodSHA: m.PeriodSHA, Sessions: development, Matrices: []studyArtifact{}, Books: []studyBook{}, NewPNL: true, Limitations: []string{"development_and_validation_only_no_holdout_pnl", "all_original_missing_cases_retained", "same_provider_price_diagnostic_not_execution_proof", "research_capital_400000_not_live_limits", "no_real_orders_or_inventory_claim", "all_cost_scenarios_preserved_no_best_scenario_selection"}}
	r.Version = reportVersion
	if round2 {
		r.Limitations = append(r.Limitations, "retrospective_2025_not_prospective_forward", "native_common_means_legacy_five_or_new_three_separately", "all_eight_and_legacy_s01_candidate_pair_common_books_reported_separately", "round2_gate_only_for_native_cohort_new_candidates_not_legacy_quality_promotion", "ordinary_margin_short_inventory_and_actual_friction_unverified")
	}
	r.FreezeSHA = freezeSHA
	r.HoldoutRead = *phase == "holdout"
	if r.HoldoutRead {
		r.Limitations[0] = "one_frozen_holdout_version_no_retuning"
	}
	modelCases := map[string][]researchbook.Case{}
	cohorts := map[string]round2Cohorts{}
	seen := map[string]bool{}
	stockSeen := map[string]bool{}
	for _, file := range idx.Files {
		if ctx.Err() != nil {
			return errors.New("一括研究を中断しました")
		}
		if stockSeen[file.Symbol] {
			return errors.New("指標の銘柄が重複しています")
		}
		stockSeen[file.Symbol] = true
		sRaw, err := pinnedInput(file.Path, file.SHA)
		if err != nil {
			return err
		}
		var stock s01StockInputs
		if strictJSON(sRaw, &stock) != nil || stock.Symbol != file.Symbol || stock.Version != stockVersion || stock.PlanSHA != prep.PlanSHA || stock.UniverseSHA != prep.UniverseSHA || stock.Quality || stock.NewPNL || stock.TradingEligible || len(stock.Cases) != file.Cases {
			return errors.New("未昇格の銘柄別指標を確認してください")
		}
		monthly := []marketdata.Dataset{}
		for _, f := range stock.Files {
			if original, ok := normalizedByKey[f.Symbol+"/"+f.Month]; !ok || original != f || f.Symbol != stock.Symbol {
				return errors.New("銘柄別指標と元の分足索引が一致しません")
			}
			body, err := pinnedInput(f.Path, f.SHA)
			if err != nil {
				return err
			}
			var d marketdata.Dataset
			if strictJSON(body, &d) != nil || d.Symbol != f.Symbol || d.Source.SHA256 != f.SourceSHA {
				return errors.New("分足の銘柄/出典を確認してください")
			}
			monthly = append(monthly, d)
		}
		joined, err := buildbars.JoinJQuants(ctx, monthly)
		if err != nil {
			return err
		}
		if joined.Source.SHA256 != stock.JoinedSource.SHA256 || joined.Source.Format != stock.JoinedSource.Format || !joined.Source.ReceivedAt.Equal(stock.JoinedSource.ReceivedAt) {
			return errors.New("月次結合の出典が変わっています")
		}
		days := map[string]marketdata.Day{}
		for _, d := range joined.Days {
			days[d.Date] = d
		}
		matrix := studyMatrix{Version: r.Version, Symbol: stock.Symbol, InputSHA: file.SHA, Models: models, Rows: []studyMatrixRow{}, NewPNL: true}
		matrix.HoldoutRead = r.HoldoutRead
		for _, c := range stock.Cases {
			key := c.Date + "/" + stock.Symbol
			if rank, ok := expected[key]; !ok || rank != c.LiquidityRank || seen[key] {
				return errors.New("指標行列の日付/銘柄/順位が一致しません")
			}
			seen[key] = true
			if c.Opening.QualityPromoted || c.Opening.TradingEligible || c.Opening.StrategyAdopted || c.ATR.QualityPromoted || c.ATR.Version != s01.Version {
				return errors.New("元の指標の品質/戦略版を確認してください")
			}
			// Route only the selected frozen phase to either signal engine.
			if !allowed[c.Date] {
				continue
			}
			subset := joined
			if day, ok := days[c.Date]; ok {
				subset.Days = []marketdata.Day{day}
			}
			ref, ok := daily[strings.TrimSuffix(stock.Symbol, "@1")+"0/"+c.Date]
			if !ok {
				return errors.New("比較対象の日足がありません")
			}
			window, err := marketdata.CheckJQuantsWindow(ctx, subset, c.Date, ref)
			if err != nil {
				return err
			}
			legacy := c
			if round2 {
				legacy, err = round2LegacyInput(c)
				if err != nil {
					return err
				}
			}
			rows := []researchbook.Case{{Date: c.Date, Symbol: stock.Symbol, Rank: c.LiquidityRank, Status: "unevaluable", Reasons: append(append([]string{}, legacy.Problems...), c.ExPostIssues...)}}
			if legacy.InputsAvailable {
				if legacy.Opening.Target.Range == nil || legacy.Opening.RelativeVolume == nil || !legacy.Opening.CalculationAvailable || c.ATR.StopDistance == nil || !c.ATR.Available || c.ATR.Symbol != stock.Symbol || c.ATR.Date != c.Date || legacy.Opening.Symbol != stock.Symbol || legacy.Opening.Target.Date != c.Date {
					return errors.New("計算可能なS01指標の内容を確認してください")
				}
				o := legacy.Opening.Target.Range
				result, err := backtests01.DiagnoseJQuants(ctx, subset, c.Date, ref, s01.Range{Open: o.Open, High: o.High, Low: o.Low, Close: o.Close}, *c.ATR.StopDistance, *legacy.Opening.RelativeVolume)
				if err != nil {
					return err
				}
				rows[0].Status = result.Status
				rows[0].Reasons = result.Reasons
				if result.Status == "traded" {
					rows[0].Status = "trade"
				}
				if result.Trade != nil {
					t := result.Trade
					rows[0].Trade = &researchbook.Trade{Direction: string(t.Direction), EntryAt: t.EntryAt, ExitAt: t.ExitAt, EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice, Reason: t.Reason}
				}
			}
			for _, v := range []r001.Variant{r001.A, r001.B} {
				for _, direction := range []r001.Direction{r001.Long, r001.Short} {
					result, err := backtest.DiagnoseJQuants(ctx, subset, c.Date, ref, direction, v)
					if err != nil {
						return err
					}
					day := result.Days[0]
					row := researchbook.Case{Date: c.Date, Symbol: stock.Symbol, Rank: c.LiquidityRank, Status: day.Status, Reasons: day.Reasons}
					if row.Status == "no_signal" || row.Status == "filtered" {
						row.Status = "no_trade"
					}
					if day.Trade != nil {
						t := day.Trade
						row.Trade = &researchbook.Trade{Direction: string(direction), EntryAt: t.EntryAt, ExitAt: t.ExitAt, EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice, Reason: t.Reason}
					}
					rows = append(rows, row)
				}
			}
			if round2 {
				for _, id := range models[5:] {
					row, err := round2PriceCase(ctx, subset, c, ref, id)
					if err != nil {
						return err
					}
					rows = append(rows, row)
				}
			}
			common := true
			for _, row := range rows {
				common = common && row.Status != "unevaluable"
			}
			var cohort *round2Cohorts
			if round2 {
				value, err := round2CaseCohorts(rows)
				if err != nil {
					return err
				}
				cohorts[key], cohort = value, &value
			}
			for i := range rows {
				rows[i].Common = common
				if round2 {
					rows[i].Common = cohort.Legacy
					if i >= 5 {
						rows[i].Common = cohort.New
					}
				}
				modelCases[models[i]] = append(modelCases[models[i]], rows[i])
			}
			matrix.Rows = append(matrix.Rows, studyMatrixRow{Date: c.Date, Rank: c.LiquidityRank, Window: window, Models: rows, PriorActions: c.PriorVolumeActionDates, Cohorts: cohort})
		}
		path, body, err := saveContentAddressed(matrixPrefix, matrix)
		if err != nil {
			return err
		}
		r.Matrices = append(r.Matrices, studyArtifact{path, hash(body)})
	}
	if len(seen) != len(expected) {
		return errors.New("比較行列に日付/銘柄の不足があります")
	}
	for _, model := range models {
		if len(modelCases[model]) != len(development)*30 {
			return errors.New("開発/検証の完全な行列を確認してください")
		}
		sort.Slice(modelCases[model], func(i, j int) bool {
			a, b := modelCases[model][i], modelCases[model][j]
			if a.Date != b.Date {
				return a.Date < b.Date
			}
			return a.Symbol < b.Symbol
		})
		for _, partition := range enabledParts {
			selected := []researchbook.Case{}
			for _, c := range modelCases[model] {
				if c.Date >= parts[partition][0] && c.Date <= parts[partition][len(parts[partition])-1] {
					selected = append(selected, c)
				}
			}
			comparisonCohorts := []string{""}
			if round2 {
				comparisonCohorts = round2BookCohorts(model)
			}
			for _, cohortName := range comparisonCohorts {
				cohortSelected := selected
				if round2 && cohortName != "native" {
					cohortSelected = round2SelectCohort(selected, cohorts, cohortName)
				}
				for _, common := range []bool{false, true} {
					if round2 && cohortName != "native" && !common {
						continue
					}
					for _, bps := range []int64{0, 5, 10, 20} {
						for _, commission := range []string{"sor_conditional_zero", "non_sor_standard"} {
							for _, extra := range []int64{0, 100, 500} {
								report, err := researchbook.Run(ctx, cohortSelected, sessions, parts[partition], researchbook.Scenario{CommonOnly: common, FrictionBPS: bps, Commission: commission, ShortExtra: extra})
								if err != nil {
									return err
								}
								path, body, err := saveContentAddressed(bookPrefix, report)
								if err != nil {
									return err
								}
								report.Ledger = nil
								book := studyBook{Model: model, Partition: partition, Artifact: studyArtifact{path, hash(body)}, Summary: report}
								if round2 {
									book.Cohort = cohortName
									if cohortName == "native" && strings.HasPrefix(model, "R2-") {
										gate, err := round2Gate(report)
										if err != nil {
											return err
										}
										book.Round2Gate = &gate
									}
								}
								r.Books = append(r.Books, book)
							}
						}
					}
				}
			}
		}
	}
	path, body, err := save(reportPrefix, r)
	if err != nil {
		return err
	}
	record, err := recordRun(command, filepath.Base(*input), raw, hash(raw), path, body, map[string]any{"manifest": m, "phase": *phase, "final_freeze_sha256": r.FreezeSHA})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "S01比較: phase=%s / sessions=%d / model_cases=%d / books=%d / holdout_pnl_read=%t / strategy_adopted=false\n", *phase, len(development), len(development)*30*len(models), len(r.Books), r.HoldoutRead)
	for _, b := range r.Books {
		if b.Partition == displayPart && !b.Summary.Scenario.CommonOnly && b.Summary.Scenario.FrictionBPS == 10 && b.Summary.Scenario.ShortExtra == 0 {
			net := formatStudyMoney(b.Summary.AfterData)
			decision := b.Summary.Decision
			if b.Round2Gate != nil {
				decision = b.Round2Gate.Decision
			}
			fmt.Fprintf(out, "検証: model=%s / commission=%s / short_extra=0 / friction_bp=10 / executed=%d / common=%d/%d / after_data_jpy=%s / gate=%s\n", b.Model, b.Summary.Scenario.Commission, b.Summary.Executed, b.Summary.Common, b.Summary.Cases, net, decision)
		}
	}
	_, err = fmt.Fprintf(out, "保存: %s\n実行記録: %s\n", path, record)
	return err
}
