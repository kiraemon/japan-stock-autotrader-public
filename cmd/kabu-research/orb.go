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

	"github.com/kiraemon/japan-stock-autotrader/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/prepareorb"
)

type orbSource struct {
	Symbol     string `json:"symbol"`
	CSV        string `json:"csv"`
	CapturedAt string `json:"captured_at"`
}

type orbManifest struct {
	Version        int         `json:"version"`
	Environment    string      `json:"environment"`
	Date           string      `json:"date_jst"`
	Previous       []string    `json:"previous_sessions"`
	CalendarSource string      `json:"calendar_source"`
	CSVLabel       string      `json:"csv_label"`
	Sources        []orbSource `json:"sources"`
}

type orbProvenance struct {
	Symbol    string           `json:"symbol"`
	CSVFile   string           `json:"csv_file"`
	CSVSHA256 string           `json:"csv_sha256"`
	Coverage  []cohortCoverage `json:"csv_coverage"`
}

type orbReport struct {
	Version                       string              `json:"version"`
	ManifestSHA256                string              `json:"manifest_sha256"`
	CalendarSource                string              `json:"calendar_source_declared"`
	CalendarAutomaticallyVerified bool                `json:"calendar_automatically_verified"`
	Environment                   string              `json:"environment"`
	Sources                       []orbProvenance     `json:"sources"`
	Inputs                        []prepareorb.Report `json:"inputs"`
	Limitations                   []string            `json:"limitations"`
}

func decodeORB(raw []byte) (orbManifest, error) {
	var m orbManifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Version != 1 || (m.Environment != "test" && m.Environment != "production") || m.CalendarSource == "" || (m.CSVLabel != "start" && m.CSVLabel != "end") || len(m.Sources) == 0 || len(m.Sources) > 50 {
		return m, errors.New("ORB入力manifestの版・環境・日付・カレンダー・元入力を確認してください")
	}
	if err := prepareorb.ValidateSessions(m.Date, m.Previous); err != nil {
		return m, err
	}
	sort.Slice(m.Sources, func(i, j int) bool { return m.Sources[i].Symbol < m.Sources[j].Symbol })
	pattern := regexp.MustCompile(`^[0-9A-Z]{4}@1$`)
	for i, s := range m.Sources {
		at, err := time.Parse(time.RFC3339, s.CapturedAt)
		if !pattern.MatchString(s.Symbol) || s.CSV == "" || err != nil || at.IsZero() || (i > 0 && m.Sources[i-1].Symbol == s.Symbol) {
			return m, errors.New("ORB入力の東証銘柄・元CSV・取得日時・重複を確認してください")
		}
	}
	return m, nil
}

func runORB(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("orb-input-check", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "評価日・直前14営業日・元CSVを固定したmanifest（オフライン診断専用）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *input == "" {
		return errors.New("orb-input-checkにはinputのmanifestだけを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	m, err := decodeORB(raw)
	if err != nil {
		return err
	}
	r := orbReport{Version: prepareorb.Version, ManifestSHA256: hash(raw), CalendarSource: m.CalendarSource, Environment: m.Environment, Sources: []orbProvenance{}, Inputs: []prepareorb.Report{}, Limitations: []string{
		"opening_ohlcv_only_not_strategy_or_backtest", "calendar_is_caller_declared_not_automatically_verified", "source_quality_not_promoted", "atr_daily_history_and_historical_universe_unverified", "no_ranking_or_execution_or_pnl", "no_strategy_adoption_or_order_capability",
	}}
	for _, s := range m.Sources {
		if ctx.Err() != nil {
			return errors.New("ORB入力検査を中断しました")
		}
		path := s.CSV
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(*input), path)
		}
		csvRaw, err := readInput(path)
		if err != nil {
			return err
		}
		at, _ := time.Parse(time.RFC3339, s.CapturedAt)
		bars, err := buildbars.FromCSV(ctx, csvRaw, s.Symbol, m.Environment, m.CSVLabel, at)
		if err != nil {
			return err
		}
		checked, err := prepareorb.Check(ctx, bars, m.Date, m.Previous)
		if err != nil {
			return err
		}
		r.Sources = append(r.Sources, orbProvenance{Symbol: s.Symbol, CSVFile: filepath.Base(path), CSVSHA256: hash(csvRaw), Coverage: coverage(bars)})
		r.Inputs = append(r.Inputs, checked)
	}
	if ctx.Err() != nil {
		return errors.New("ORB入力検査を中断しました")
	}
	path, outputRaw, err := save("orb-input", r)
	if err != nil {
		return err
	}
	runPath, err := recordRun("orb-input-check", filepath.Base(*input), raw, hash(raw), path, outputRaw, m)
	if err != nil {
		return err
	}
	for _, row := range r.Inputs {
		if _, err := fmt.Fprintf(out, "ORB入力: symbol=%s / date=%s / previous_complete=%d/14 / previous_unavailable=%d / target_complete=%t / calculation_available=%t\n", row.Symbol, m.Date, row.CompletePrevious, len(row.MissingPrevious), row.Target.Volume != nil, row.CalculationAvailable); err != nil {
			return errors.New("ORB入力結果を出力できません")
		}
	}
	_, err = fmt.Fprintf(out, "strategy_adopted=false / trading_eligible=false / quality_promoted=false / backtest=false\n保存: %s\n実行記録: %s\n", path, runPath)
	return err
}
