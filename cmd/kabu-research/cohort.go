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
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/reviewstrategies"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

type cohortSource struct {
	Symbol     string `json:"symbol"`
	CSV        string `json:"csv"`
	History    string `json:"history"`
	CapturedAt string `json:"captured_at"`
}

type cohortManifest struct {
	Version     int            `json:"version"`
	Environment string         `json:"environment"`
	Dates       []string       `json:"dates"`
	Sources     []cohortSource `json:"sources"`
}

type cohortCoverage struct {
	Date  string `json:"date_jst"`
	Bars  int    `json:"regular_minutes"`
	First string `json:"first_minute_jst,omitempty"`
	Last  string `json:"last_minute_jst,omitempty"`
}

type cohortProvenance struct {
	Symbol          string           `json:"symbol"`
	CSVFile         string           `json:"csv_file"`
	HistoryFile     string           `json:"history_file"`
	CSVSHA256       string           `json:"csv_sha256"`
	HistorySHA256   string           `json:"history_sha256"`
	CapturedAt      string           `json:"captured_at"`
	CSVCoverage     []cohortCoverage `json:"csv_coverage"`
	HistoryCoverage []cohortCoverage `json:"history_coverage"`
}

type cohortReport struct {
	Version        string                  `json:"version"`
	ManifestSHA256 string                  `json:"manifest_sha256"`
	Environment    string                  `json:"environment"`
	Sources        []cohortProvenance      `json:"sources"`
	Diagnostics    []backtest.Report       `json:"diagnostics"`
	Summary        reviewstrategies.Report `json:"summary"`
	Limitations    []string                `json:"limitations"`
}

func decodeCohort(raw []byte) (cohortManifest, error) {
	var m cohortManifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Version != 1 || (m.Environment != "test" && m.Environment != "production") || len(m.Dates) == 0 || len(m.Dates) > 500 || len(m.Sources) == 0 || len(m.Sources) > 50 {
		return m, errors.New("比較manifestの版・環境・日付・元入力を確認してください")
	}
	sort.Strings(m.Dates)
	for i, date := range m.Dates {
		at, err := time.Parse("2006-01-02", date)
		if err != nil || at.Format("2006-01-02") != date || (i > 0 && m.Dates[i-1] == date) {
			return m, errors.New("比較日付は重複しないYYYY-MM-DDで指定してください")
		}
	}
	sort.Slice(m.Sources, func(i, j int) bool { return m.Sources[i].Symbol < m.Sources[j].Symbol })
	pattern := regexp.MustCompile(`^[0-9A-Z]{4}@1$`)
	for i, s := range m.Sources {
		_, err := time.Parse(time.RFC3339, s.CapturedAt)
		if !pattern.MatchString(s.Symbol) || s.CSV == "" || s.History == "" || err != nil || (i > 0 && m.Sources[i-1].Symbol == s.Symbol) {
			return m, errors.New("銘柄は重複のない東証コード、元入力と取得日時を指定してください")
		}
	}
	return m, nil
}

func coverage(d marketdata.Dataset) []cohortCoverage {
	result := []cohortCoverage{}
	for _, day := range d.Days {
		v := cohortCoverage{Date: day.Date, Bars: len(day.Bars)}
		if len(day.Bars) > 0 {
			v.First = day.Bars[0].Start.In(marketdata.JST).Format("15:04")
			v.Last = day.Bars[len(day.Bars)-1].Start.In(marketdata.JST).Format("15:04")
		}
		result = append(result, v)
	}
	return result
}

// No bars are invented for absent dates. An explicit empty, unverified day
// makes the fixed matrix retain missing days and the diagnostic fail closed.
func cohortDates(d marketdata.Dataset, dates []string) marketdata.Dataset {
	byDate := map[string]marketdata.Day{}
	for _, day := range d.Days {
		byDate[day.Date] = day
	}
	d.Days = []marketdata.Day{}
	for _, date := range dates {
		day, exists := byDate[date]
		if !exists {
			day = marketdata.Day{Date: date, Issues: []string{"source_date_absent"}, Bars: []marketdata.Bar{}, Events: []marketdata.Event{}}
		}
		d.Days = append(d.Days, day)
	}
	return d
}

func cohortTrade(t *backtest.Trade) *reviewstrategies.Trade {
	if t == nil {
		return nil
	}
	return &reviewstrategies.Trade{Entry: t.EntryPrice, Exit: t.ExitPrice}
}

func runCohort(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("cohort-check", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "固定日付・銘柄と元CSV/歩み値のmanifest（オフライン専用）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *input == "" {
		return errors.New("cohort-checkにはinputのmanifestだけを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	m, err := decodeCohort(raw)
	if err != nil {
		return err
	}
	r := cohortReport{Version: reviewstrategies.Version, ManifestSHA256: hash(raw), Environment: m.Environment, Sources: []cohortProvenance{}, Diagnostics: []backtest.Report{}, Limitations: []string{
		"same_provider_price_crosscheck_only", "next_minute_open_execution_assumed", "friction_is_sensitivity_not_measured_cost", "actual_credit_inventory_and_costs_unverified", "not_portfolio_or_account_returns", "not_forward_test_or_verified_unseen_holdout", "no_strategy_adoption_or_order_capability",
	}}
	rows, symbols := []reviewstrategies.Day{}, []string{}
	baseDir := filepath.Dir(*input)
	for _, s := range m.Sources {
		if ctx.Err() != nil {
			return errors.New("戦略比較を中断しました")
		}
		resolve := func(path string) string {
			if filepath.IsAbs(path) {
				return path
			}
			return filepath.Join(baseDir, path)
		}
		csvRaw, err := readInput(resolve(s.CSV))
		if err != nil {
			return err
		}
		historyRaw, err := readInput(resolve(s.History))
		if err != nil {
			return err
		}
		at, _ := time.Parse(time.RFC3339, s.CapturedAt)
		csv, err := buildbars.FromCSV(ctx, csvRaw, s.Symbol, m.Environment, "start", at)
		if err != nil {
			return err
		}
		history, err := buildbars.FromHistory(ctx, historyRaw)
		if err != nil {
			return err
		}
		if history.Symbol != s.Symbol || history.Environment != m.Environment {
			return errors.New("manifestと履歴の銘柄/環境が一致しません")
		}
		r.Sources = append(r.Sources, cohortProvenance{Symbol: s.Symbol, CSVFile: filepath.Base(s.CSV), HistoryFile: filepath.Base(s.History), CSVSHA256: hash(csvRaw), HistorySHA256: hash(historyRaw), CapturedAt: s.CapturedAt, CSVCoverage: coverage(csv), HistoryCoverage: coverage(history)})
		symbols = append(symbols, s.Symbol)
		csv, history = cohortDates(csv, m.Dates), cohortDates(history, m.Dates)
		for _, variant := range []r001.Variant{r001.A, r001.B} {
			for _, direction := range []r001.Direction{r001.Long, r001.Short} {
				diagnostic, err := backtest.DiagnoseVariant(ctx, csv, history, direction, variant)
				if err != nil {
					return err
				}
				r.Diagnostics = append(r.Diagnostics, diagnostic)
				for _, day := range diagnostic.Days {
					d := reviewstrategies.Day{Symbol: s.Symbol, Date: day.Date, Direction: direction, Variant: variant, Status: day.Status}
					if day.Status != "unevaluable" {
						d.Trade, d.Baseline = cohortTrade(day.Trade), cohortTrade(day.Baseline)
					}
					rows = append(rows, d)
				}
			}
		}
	}
	r.Summary, err = reviewstrategies.Review(ctx, m.Dates, symbols, rows)
	if err != nil {
		return err
	}
	path, outputRaw, err := save("cohort-review", r)
	if err != nil {
		return err
	}
	manifest, err := recordRun("cohort-check", filepath.Base(*input), raw, hash(raw), path, outputRaw, struct {
		Version  string         `json:"version"`
		Manifest cohortManifest `json:"manifest"`
	}{reviewstrategies.Version, m})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "固定戦略比較: symbols=%d / dates=%d / diagnostic_cases=%d\n", len(symbols), len(m.Dates), len(rows)); err != nil {
		return errors.New("研究結果を出力できません")
	}
	for _, g := range r.Summary.Groups {
		mean := "unavailable"
		if g.MeanTrade != nil {
			mean = *g.MeanTrade
		}
		if _, err := fmt.Fprintf(out, "variant=%s / direction=%s / friction_bps=%d / evaluable=%d / unevaluable=%d / trades=%d / filtered=%d / mean_net_bps=%s\n", g.Variant, g.Direction, g.CostBPS, g.Eligible, g.Unevaluable, g.Trades, g.Filtered, mean); err != nil {
			return errors.New("研究結果を出力できません")
		}
	}
	_, err = fmt.Fprintf(out, "strategy_adopted=false / trading_eligible=false / portfolio_mode=false / forward_test=false\n保存: %s\n実行記録: %s\n", path, manifest)
	return err
}
