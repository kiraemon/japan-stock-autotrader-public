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

	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtests01"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/researchbook"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
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
	Model     string              `json:"model"`
	Partition string              `json:"partition"`
	Artifact  studyArtifact       `json:"artifact"`
	Summary   researchbook.Report `json:"summary"`
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
	fs := flag.NewFlagSet("study-s01", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "指標索引/入力manifest/期間計画をhash固定した研究manifest")
	phase := fs.String("phase", "development", "developmentのみ。最終評価の損益は開かない")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *phase != "development" {
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
	if strictJSON(prepRaw, &prep) != nil || prep.Version != 1 || prep.PlanSHA != s01.PlanSHA256 {
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
	if strictJSON(iRaw, &idx) != nil || idx.Version != "s01-input-index-v0.1" || idx.PlanSHA != prep.PlanSHA || idx.ManifestSHA != m.PreparationSHA || idx.UniverseSHA != prep.UniverseSHA || idx.NormalizationSHA != prep.NormalizationSHA || idx.ReconciliationSHA != prep.ReconciliationSHA || idx.Quality || idx.NewPNL || idx.TradingEligible {
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
	parts, err := studyPeriods(pRaw, sessions)
	if err != nil {
		return err
	}
	if idx.Cases != 5400 || len(expected) != 5400 || len(idx.Files) != len(u.Universe.Union) {
		return errors.New("全日付/銘柄の指標行列を確認してください")
	}
	development := append(append([]string{}, parts["train"]...), parts["validation"]...)
	allowed := map[string]bool{}
	for _, date := range development {
		allowed[date] = true
	}
	r := studyReport{Version: "s01-comparative-study-v0.1", Phase: *phase, ManifestSHA: hash(raw), PlanSHA: prep.PlanSHA, IndicatorsSHA: m.IndicatorsSHA, PeriodSHA: m.PeriodSHA, Sessions: development, Matrices: []studyArtifact{}, Books: []studyBook{}, NewPNL: true, Limitations: []string{"development_and_validation_only_no_holdout_pnl", "all_original_missing_cases_retained", "same_provider_price_diagnostic_not_execution_proof", "research_capital_400000_not_live_limits", "no_real_orders_or_inventory_claim", "all_cost_scenarios_preserved_no_best_scenario_selection"}}
	modelCases := map[string][]researchbook.Case{}
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
		if strictJSON(sRaw, &stock) != nil || stock.Symbol != file.Symbol || stock.Version != "s01-stock-input-v0.1" || stock.PlanSHA != prep.PlanSHA || stock.UniverseSHA != prep.UniverseSHA || stock.Quality || stock.NewPNL || stock.TradingEligible || len(stock.Cases) != file.Cases {
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
		matrix := studyMatrix{Version: r.Version, Symbol: stock.Symbol, InputSHA: file.SHA, Models: studyModels, Rows: []studyMatrixRow{}, NewPNL: true}
		for _, c := range stock.Cases {
			key := c.Date + "/" + stock.Symbol
			if rank, ok := expected[key]; !ok || rank != c.LiquidityRank || seen[key] {
				return errors.New("指標行列の日付/銘柄/順位が一致しません")
			}
			seen[key] = true
			if c.Opening.QualityPromoted || c.Opening.TradingEligible || c.Opening.StrategyAdopted || c.ATR.QualityPromoted || c.ATR.Version != s01.Version {
				return errors.New("元の指標の品質/戦略版を確認してください")
			}
			// Do not invoke either signal engine for ANY holdout row.
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
			rows := []researchbook.Case{{Date: c.Date, Symbol: stock.Symbol, Rank: c.LiquidityRank, Status: "unevaluable", Reasons: append(append([]string{}, c.Problems...), c.ExPostIssues...)}}
			if c.InputsAvailable {
				if c.Opening.Target.Range == nil || c.Opening.RelativeVolume == nil || !c.Opening.CalculationAvailable || c.ATR.StopDistance == nil || !c.ATR.Available || c.ATR.Symbol != stock.Symbol || c.ATR.Date != c.Date || c.Opening.Symbol != stock.Symbol || c.Opening.Target.Date != c.Date {
					return errors.New("計算可能なS01指標の内容を確認してください")
				}
				o := c.Opening.Target.Range
				result, err := backtests01.DiagnoseJQuants(ctx, subset, c.Date, ref, s01.Range{Open: o.Open, High: o.High, Low: o.Low, Close: o.Close}, *c.ATR.StopDistance, *c.Opening.RelativeVolume)
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
			common := true
			for _, row := range rows {
				common = common && row.Status != "unevaluable"
			}
			for i := range rows {
				rows[i].Common = common
				modelCases[studyModels[i]] = append(modelCases[studyModels[i]], rows[i])
			}
			matrix.Rows = append(matrix.Rows, studyMatrixRow{Date: c.Date, Rank: c.LiquidityRank, Window: window, Models: rows, PriorActions: c.PriorVolumeActionDates})
		}
		path, body, err := saveContentAddressed("study-cases", matrix)
		if err != nil {
			return err
		}
		r.Matrices = append(r.Matrices, studyArtifact{path, hash(body)})
	}
	if len(seen) != len(expected) {
		return errors.New("比較行列に日付/銘柄の不足があります")
	}
	for _, model := range studyModels {
		if len(modelCases[model]) != 144*30 {
			return errors.New("開発/検証の完全な行列を確認してください")
		}
		sort.Slice(modelCases[model], func(i, j int) bool {
			a, b := modelCases[model][i], modelCases[model][j]
			if a.Date != b.Date {
				return a.Date < b.Date
			}
			return a.Symbol < b.Symbol
		})
		for _, partition := range []string{"train", "validation"} {
			selected := []researchbook.Case{}
			for _, c := range modelCases[model] {
				if c.Date >= fixedPartitions[partition].From && c.Date <= fixedPartitions[partition].To {
					selected = append(selected, c)
				}
			}
			for _, common := range []bool{false, true} {
				for _, bps := range []int64{0, 5, 10, 20} {
					for _, commission := range []string{"sor_conditional_zero", "non_sor_standard"} {
						for _, extra := range []int64{0, 100, 500} {
							report, err := researchbook.Run(ctx, selected, sessions, parts[partition], researchbook.Scenario{CommonOnly: common, FrictionBPS: bps, Commission: commission, ShortExtra: extra})
							if err != nil {
								return err
							}
							path, body, err := saveContentAddressed("study-book", report)
							if err != nil {
								return err
							}
							report.Ledger = nil
							r.Books = append(r.Books, studyBook{model, partition, studyArtifact{path, hash(body)}, report})
						}
					}
				}
			}
		}
	}
	path, body, err := save("s01-study", r)
	if err != nil {
		return err
	}
	record, err := recordRun("study-s01", filepath.Base(*input), raw, hash(raw), path, body, m)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "S01比較: phase=development / sessions=144 / model_cases=21600 / books=%d / holdout_pnl_read=false / strategy_adopted=false\n", len(r.Books))
	for _, b := range r.Books {
		if b.Partition == "validation" && !b.Summary.Scenario.CommonOnly && b.Summary.Scenario.FrictionBPS == 10 && b.Summary.Scenario.ShortExtra == 0 {
			net := formatStudyMoney(b.Summary.AfterData)
			fmt.Fprintf(out, "検証: model=%s / commission=%s / short_extra=0 / friction_bp=10 / executed=%d / common=%d/%d / after_data_jpy=%s / gate=%s\n", b.Model, b.Summary.Scenario.Commission, b.Summary.Executed, b.Summary.Common, b.Summary.Cases, net, b.Summary.Decision)
		}
	}
	_, err = fmt.Fprintf(out, "保存: %s\n実行記録: %s\n", path, record)
	return err
}
