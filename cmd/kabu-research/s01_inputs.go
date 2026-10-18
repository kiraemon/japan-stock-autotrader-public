package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/prepareorb"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

type s01InputManifest struct {
	Version           int    `json:"version"`
	Universe          string `json:"universe_report"`
	UniverseSHA       string `json:"universe_sha256"`
	Normalization     string `json:"normalization_index"`
	NormalizationSHA  string `json:"normalization_sha256"`
	Reconciliation    string `json:"daily_reconciliation"`
	ReconciliationSHA string `json:"reconciliation_sha256"`
	Plan              string `json:"strategy_plan"`
	PlanSHA           string `json:"strategy_plan_sha256"`
}
type s01MinuteFile struct {
	Month     string `json:"month"`
	Symbol    string `json:"symbol"`
	Path      string `json:"path"`
	SHA       string `json:"sha256"`
	SourceSHA string `json:"source_sha256"`
	Days      int    `json:"days"`
	Bars      int    `json:"bars"`
}
type s01Normalization struct {
	Version     int             `json:"version"`
	Universe    string          `json:"universe"`
	UniverseSHA string          `json:"universe_sha256"`
	Records     []s01MinuteFile `json:"records"`
	Complete    bool            `json:"complete"`
	NewPNL      bool            `json:"new_pnl_evaluation"`
	Quality     bool            `json:"quality_promoted"`
}
type s01InputCase struct {
	Date                   string            `json:"date_jst"`
	LiquidityRank          int               `json:"liquidity_rank"`
	Opening                prepareorb.Report `json:"opening"`
	ATR                    s01.ATRReport     `json:"atr"`
	InputsAvailable        bool              `json:"indicators_available"`
	RvolPassed             bool              `json:"rvol_at_least_one"`
	PriorVolumeActionDates []string          `json:"raw_volume_reference_action_dates"`
	ExPostIssues           []string          `json:"ex_post_input_issues"`
	Problems               []string          `json:"problems"`
}
type s01StockInputs struct {
	Version         string            `json:"version"`
	Symbol          string            `json:"symbol"`
	PlanSHA         string            `json:"strategy_plan_sha256"`
	UniverseSHA     string            `json:"universe_sha256"`
	JoinedSource    marketdata.Source `json:"joined_source"`
	Files           []s01MinuteFile   `json:"minute_files"`
	Cases           []s01InputCase    `json:"cases"`
	Quality         bool              `json:"quality_promoted"`
	TradingEligible bool              `json:"trading_eligible"`
	NewPNL          bool              `json:"new_pnl_evaluation"`
}
type s01InputFile struct {
	Symbol     string `json:"symbol"`
	Path       string `json:"path"`
	SHA        string `json:"sha256"`
	Cases      int    `json:"cases"`
	Available  int    `json:"indicators_available"`
	RvolPassed int    `json:"rvol_passed"`
}
type s01InputIndex struct {
	Version           string         `json:"version"`
	ManifestSHA       string         `json:"manifest_sha256"`
	PlanSHA           string         `json:"strategy_plan_sha256"`
	UniverseSHA       string         `json:"universe_sha256"`
	NormalizationSHA  string         `json:"normalization_sha256"`
	ReconciliationSHA string         `json:"reconciliation_sha256"`
	DailySHA          string         `json:"daily_catalog_sha256"`
	Files             []s01InputFile `json:"files"`
	Cases             int            `json:"cases"`
	Available         int            `json:"indicators_available"`
	RvolPassed        int            `json:"rvol_passed"`
	Limitations       []string       `json:"limitations"`
	TradingEligible   bool           `json:"trading_eligible"`
	Quality           bool           `json:"quality_promoted"`
	NewPNL            bool           `json:"new_pnl_evaluation"`
}

func pinnedInput(path, expected string) ([]byte, error) {
	raw, err := readInput(path)
	if err != nil {
		return nil, err
	}
	if len(expected) != 64 || hash(raw) != expected {
		return nil, errors.New("固定した研究入力のhashが一致しません")
	}
	return raw, nil
}

func runS01Inputs(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("prepare-s01", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "固定済み母集団・分足・日足照合・戦略条件のmanifest（指標準備のみ）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *input == "" {
		return errors.New("prepare-s01にはinputのmanifestを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	var m s01InputManifest
	if strictJSON(raw, &m) != nil || m.Version != 1 {
		return errors.New("S01入力manifestの版/形式を確認してください")
	}
	resolve := func(path string) string { return relativeORBPath(*input, path) }
	planRaw, err := pinnedInput(resolve(m.Plan), m.PlanSHA)
	if err != nil {
		return err
	}
	if hash(planRaw) != s01.PlanSHA256 {
		return errors.New("実装したS01条件版とJSONのhashが一致しません")
	}
	uRaw, err := pinnedInput(resolve(m.Universe), m.UniverseSHA)
	if err != nil {
		return err
	}
	var u universeOutput
	if strictJSON(uRaw, &u) != nil || u.Version != "s01-universe-v0.1" || u.Universe.Version != u.Version || u.Universe.StrategyAdopted || u.Universe.QualityPromoted || u.Universe.TradingEligible || u.Universe.NewPNL || len(u.Universe.Days) == 0 {
		return errors.New("未昇格の検証済み母集団レポートを確認してください")
	}
	nRaw, err := pinnedInput(resolve(m.Normalization), m.NormalizationSHA)
	if err != nil {
		return err
	}
	var n s01Normalization
	if strictJSON(nRaw, &n) != nil || n.Version != 1 || !n.Complete || n.NewPNL || n.Quality || n.UniverseSHA != m.UniverseSHA || len(n.Records) == 0 || len(n.Records) > 10000 {
		return errors.New("同じ母集団の完了した分足索引を確認してください")
	}
	cRaw, err := pinnedInput(resolve(m.Reconciliation), m.ReconciliationSHA)
	if err != nil {
		return err
	}
	var reconciliation struct {
		Version      string `json:"version"`
		IndexSHA     string `json:"normalization_index_sha256"`
		DailySHA     string `json:"daily_catalog_sha256"`
		Files        int    `json:"files"`
		Days         int    `json:"matched_stock_days"`
		Bars         int    `json:"bars"`
		Events       int    `json:"boundary_events"`
		BoundaryOnly int    `json:"boundary_only_stock_days"`
		Issues       []any  `json:"issues"`
		Quality      bool   `json:"quality_promoted"`
		PNL          bool   `json:"new_pnl_evaluation"`
	}
	if strictJSON(cRaw, &reconciliation) != nil || reconciliation.Version != "daily-minute-reconciliation-v0.2" || reconciliation.IndexSHA != m.NormalizationSHA || reconciliation.DailySHA != u.DailySHA || reconciliation.Files != len(n.Records) || len(reconciliation.Issues) > 0 || reconciliation.Quality || reconciliation.PNL {
		return errors.New("日足/分足の一致した未昇格の照合記録を確認してください")
	}
	totalDays, totalBars := 0, 0
	for _, file := range n.Records {
		if file.Days < 1 || file.Days > 31 || file.Bars < 0 || file.Bars > 31*325 {
			return errors.New("分足索引の日数/本数の範囲を確認してください")
		}
		totalDays += file.Days
		totalBars += file.Bars
	}
	if totalDays != reconciliation.Days || totalBars != reconciliation.Bars {
		return errors.New("日足/分足の照合件数と索引が一致しません")
	}
	dRaw, err := pinnedInput(u.DailyCatalog, u.DailySHA)
	if err != nil {
		return err
	}
	var daily universeDailyCatalog
	if strictJSON(dRaw, &daily) != nil || daily.Version != "jquants-unadjusted-daily-v0.1" || daily.Origin != "observed" || daily.PriceScale != marketdata.PriceScale || daily.QualityPromoted {
		return errors.New("検査済み未調整日足の契約を確認してください")
	}
	calendars := map[string][]string{}
	for _, source := range u.Sources {
		if source.Kind != "calendar" {
			continue
		}
		capture, p, err := readUniverseCapture(source.Capture, "calendar")
		if err != nil {
			return err
		}
		if p.CaptureSHA != source.CaptureSHA || p.Month != source.Month {
			return errors.New("母集団の公式カレンダーが変更されています")
		}
		if _, exists := calendars[p.Month]; exists {
			return errors.New("カレンダー月が重複しています")
		}
		sessions, err := marketdata.JQuantsSessions(capture.Calendar, p.Month)
		if err != nil {
			return err
		}
		calendars[p.Month] = sessions
	}
	months := []string{}
	for month := range calendars {
		months = append(months, month)
	}
	sort.Strings(months)
	sessions := []string{}
	for i, month := range months {
		if i > 0 {
			previous, err := time.Parse("2006-01", months[i-1])
			if err != nil || previous.AddDate(0, 1, 0).Format("2006-01") != month {
				return errors.New("S01参照カレンダーの月が連続していません")
			}
		}
		sessions = append(sessions, calendars[month]...)
	}
	byCode := map[string][]marketdata.DailyBar{}
	dailyByKey := map[string]marketdata.DailyBar{}
	for _, d := range daily.Bars {
		key := d.Code + "/" + d.Date
		if _, exists := dailyByKey[key]; exists {
			return errors.New("日足カタログの日付/銘柄が重複しています")
		}
		dailyByKey[key] = d
		byCode[d.Code] = append(byCode[d.Code], d)
	}
	bySymbol := map[string][]s01MinuteFile{}
	seen := map[string]bool{}
	for _, file := range n.Records {
		key := file.Symbol + "/" + file.Month
		if seen[key] {
			return errors.New("分足索引の銘柄/月が重複しています")
		}
		seen[key] = true
		bySymbol[file.Symbol] = append(bySymbol[file.Symbol], file)
	}
	type target struct {
		date string
		rank int
	}
	targets := map[string][]target{}
	for _, day := range u.Universe.Days {
		for i, pick := range day.Picks {
			if len(pick.Code) != 5 || !strings.HasSuffix(pick.Code, "0") {
				return errors.New("普通株のコードを確認してください")
			}
			symbol := pick.Code[:4] + "@1"
			targets[symbol] = append(targets[symbol], target{day.Date, i + 1})
		}
	}
	symbols := []string{}
	for symbol := range targets {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	index := s01InputIndex{Version: "s01-input-index-v0.1", ManifestSHA: hash(raw), PlanSHA: m.PlanSHA, UniverseSHA: m.UniverseSHA, NormalizationSHA: m.NormalizationSHA, ReconciliationSHA: m.ReconciliationSHA, DailySHA: u.DailySHA, Files: []s01InputFile{}, Limitations: []string{"indicators_only_no_signals_fills_or_pnl", "raw_opening_volume_corporate_actions_reported_not_revised", "current_day_corporate_action_is_ex_post_quality_not_pretrade_information", "historical_receive_times_and_trade_eligibility_unverified", "same_provider_reconciliation_not_independent_market_verification"}}
	for _, symbol := range symbols {
		if ctx.Err() != nil {
			return errors.New("S01指標準備を中断しました")
		}
		files := bySymbol[symbol]
		sort.Slice(files, func(i, j int) bool { return files[i].Month < files[j].Month })
		monthly := []marketdata.Dataset{}
		for _, file := range files {
			body, err := pinnedInput(file.Path, file.SHA)
			if err != nil {
				return err
			}
			var bars marketdata.Dataset
			if strictJSON(body, &bars) != nil || bars.Symbol != symbol || bars.Source.SHA256 != file.SourceSHA {
				return errors.New("分足索引の銘柄/出典を確認してください")
			}
			count := 0
			for _, day := range bars.Days {
				if !strings.HasPrefix(day.Date, file.Month+"-") {
					return errors.New("分足の日付が索引の月と一致しません")
				}
				count += len(day.Bars)
			}
			if len(bars.Days) != file.Days || count != file.Bars {
				return errors.New("分足索引の日数/本数が一致しません")
			}
			monthly = append(monthly, bars)
		}
		joined, err := buildbars.JoinJQuants(ctx, monthly)
		if err != nil {
			return err
		}
		stock := s01StockInputs{Version: "s01-stock-input-v0.1", Symbol: symbol, PlanSHA: m.PlanSHA, UniverseSHA: m.UniverseSHA, JoinedSource: joined.Source, Files: files, Cases: []s01InputCase{}}
		code := strings.TrimSuffix(symbol, "@1") + "0"
		ready, passed := 0, 0
		for _, target := range targets[symbol] {
			at := sort.SearchStrings(sessions, target.date)
			if at < 15 || at >= len(sessions) || sessions[at] != target.date {
				return errors.New("S01の評価日と直前15営業日が不足しています")
			}
			previous := sessions[at-14 : at]
			wanted := map[string]bool{target.date: true}
			for _, day := range previous {
				wanted[day] = true
			}
			subset := joined
			subset.Days = []marketdata.Day{}
			for _, day := range joined.Days {
				if wanted[day.Date] {
					subset.Days = append(subset.Days, day)
				}
			}
			opening, err := prepareorb.CheckWithPolicy(ctx, subset, target.date, previous, prepareorb.JQuantsPrefixPolicy)
			if err != nil {
				return err
			}
			atr, err := s01.ATR(ctx, byCode[code], symbol, target.date, sessions[at-15:at])
			if err != nil {
				return err
			}
			row := s01InputCase{Date: target.date, LiquidityRank: target.rank, Opening: opening, ATR: atr, PriorVolumeActionDates: []string{}, ExPostIssues: []string{}, Problems: []string{}}
			for _, day := range previous {
				d, exists := dailyByKey[code+"/"+day]
				if exists && (d.AdjustmentFactor != "1" || d.ExRights != "") {
					row.PriorVolumeActionDates = append(row.PriorVolumeActionDates, day)
				}
			}
			current, exists := dailyByKey[code+"/"+target.date]
			if !exists || !current.HasTrades {
				row.ExPostIssues = append(row.ExPostIssues, "target_daily_unavailable")
			} else if current.AdjustmentFactor != "1" || current.ExRights != "" {
				row.ExPostIssues = append(row.ExPostIssues, "current_day_action_basis_unverified")
			}
			if !opening.CalculationAvailable {
				row.Problems = append(row.Problems, "opening_rvol_unavailable")
			}
			if !atr.Available || atr.StopDistance == nil || *atr.StopDistance <= 0 {
				row.Problems = append(row.Problems, "atr_or_positive_stop_unavailable")
			}
			row.InputsAvailable = len(row.Problems) == 0 && len(row.ExPostIssues) == 0
			row.RvolPassed = opening.AtLeastOne != nil && *opening.AtLeastOne
			if row.InputsAvailable {
				ready++
				if row.RvolPassed {
					passed++
				}
			}
			stock.Cases = append(stock.Cases, row)
		}
		path, body, err := saveContentAddressed("s01-inputs", stock)
		if err != nil {
			return err
		}
		index.Files = append(index.Files, s01InputFile{symbol, path, hash(body), len(stock.Cases), ready, passed})
		index.Cases += len(stock.Cases)
		index.Available += ready
		index.RvolPassed += passed
	}
	path, body, err := save("s01-input-index", index)
	if err != nil {
		return err
	}
	record, err := recordRun("prepare-s01", filepath.Base(*input), raw, hash(raw), path, body, m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "S01指標: symbols=%d / cases=%d / indicators_available=%d / rvol_passed_with_available_inputs=%d / quality_promoted=false / new_pnl_evaluation=false\n保存: %s\n実行記録: %s\n", len(index.Files), index.Cases, index.Available, index.RvolPassed, path, record)
	return err
}
