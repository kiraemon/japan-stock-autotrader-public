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

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/prepareorb"
	"github.com/kiraemon/japan-stock-trading-lab/internal/jquantsbulk"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

type orbSource struct {
	Symbol     string `json:"symbol"`
	CSV        string `json:"csv"`
	CapturedAt string `json:"captured_at"`
	BarsJSON   string `json:"bars_json,omitempty"`
}

type orbManifest struct {
	Version        int         `json:"version"`
	Environment    string      `json:"environment"`
	Date           string      `json:"date_jst"`
	Previous       []string    `json:"previous_sessions"`
	CalendarSource string      `json:"calendar_source"`
	CSVLabel       string      `json:"csv_label"`
	Sources        []orbSource `json:"sources"`
	CalendarFile   string      `json:"calendar_file,omitempty"`
	OpeningPolicy  string      `json:"opening_policy,omitempty"`
}

type orbProvenance struct {
	Symbol        string           `json:"symbol"`
	CSVFile       string           `json:"csv_file,omitempty"`
	CSVSHA256     string           `json:"csv_sha256,omitempty"`
	Coverage      []cohortCoverage `json:"csv_coverage,omitempty"`
	InputCoverage []cohortCoverage `json:"input_coverage,omitempty"`
	InputFile     string           `json:"input_file,omitempty"`
	InputSHA256   string           `json:"input_sha256,omitempty"`
}

type orbReport struct {
	Version                       string              `json:"version"`
	ManifestSHA256                string              `json:"manifest_sha256"`
	CalendarSource                string              `json:"calendar_source_declared"`
	CalendarAutomaticallyVerified bool                `json:"calendar_automatically_verified"`
	Environment                   string              `json:"environment"`
	CalendarSHA256                string              `json:"calendar_sha256,omitempty"`
	Sources                       []orbProvenance     `json:"sources"`
	Inputs                        []prepareorb.Report `json:"inputs"`
	Limitations                   []string            `json:"limitations"`
}

func decodeORB(raw []byte) (orbManifest, error) {
	var m orbManifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || (m.Version != 1 && m.Version != 2) || (m.Environment != "test" && m.Environment != "production") || m.CalendarSource == "" || len(m.Sources) == 0 || len(m.Sources) > 50 {
		return m, errors.New("ORB入力manifestの版・環境・日付・カレンダー・元入力を確認してください")
	}
	if (m.Version == 1 && (m.CalendarFile != "" || (m.CSVLabel != "start" && m.CSVLabel != "end"))) || (m.Version == 2 && (m.CalendarFile == "" || m.Environment != "production" || m.CSVLabel != "")) {
		return m, errors.New("ORB入力版ごとのCSV/公式カレンダー指定を確認してください")
	}
	if err := prepareorb.ValidateSessions(m.Date, m.Previous); err != nil {
		return m, err
	}
	if m.OpeningPolicy != "" && (m.Version != 2 || m.OpeningPolicy != prepareorb.JQuantsPrefixPolicy) {
		return m, errors.New("入力方針はmanifest v2の固定したJ-Quants研究方針だけを指定できます")
	}
	sort.Slice(m.Sources, func(i, j int) bool { return m.Sources[i].Symbol < m.Sources[j].Symbol })
	pattern := regexp.MustCompile(`^[0-9A-Z]{4}@1$`)
	for i, s := range m.Sources {
		at, err := time.Parse(time.RFC3339, s.CapturedAt)
		validSource := (m.Version == 1 && s.CSV != "" && s.BarsJSON == "" && err == nil && !at.IsZero()) || (m.Version == 2 && s.BarsJSON != "" && s.CSV == "" && s.CapturedAt == "")
		if !pattern.MatchString(s.Symbol) || !validSource || (i > 0 && m.Sources[i-1].Symbol == s.Symbol) {
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
	if m.OpeningPolicy != "" {
		r.Version = prepareorb.PolicyVersion
		r.Limitations = append(r.Limitations, "leading_absence_assumed_no_trades_not_independent_coverage_or_auction_evidence", "fixed_0900_0904_window_no_zero_fill_or_shift_or_reference_skip")
	}
	if m.Version == 2 {
		calendarRaw, err := readInput(relativeORBPath(*input, m.CalendarFile))
		if err != nil {
			return err
		}
		var capture jquantsbulk.Report
		d := json.NewDecoder(bytes.NewReader(calendarRaw))
		d.DisallowUnknownFields()
		if d.Decode(&capture) != nil || d.Decode(new(any)) != io.EOF || capture.Version != jquantsbulk.Version || !capture.Complete || !capture.CalendarOnly || capture.Captured.Before(capture.Started) || capture.Started.IsZero() {
			return errors.New("完了した公式カレンダー取得記録を確認してください")
		}
		sessions, err := marketdata.JQuantsSessions(capture.Calendar, capture.Month)
		if err != nil {
			return err
		}
		index := sort.SearchStrings(sessions, m.Date)
		if index < 14 || index >= len(sessions) || sessions[index] != m.Date {
			return errors.New("公式カレンダー内の評価日と直前14営業日が不足しています")
		}
		for i, day := range m.Previous {
			if day != sessions[index-14+i] {
				return errors.New("指定した直前14営業日が公式カレンダーと一致しません")
			}
		}
		r.CalendarSHA256 = hash(calendarRaw)
		r.CalendarAutomaticallyVerified = true
		r.Limitations[1] = "calendar_checked_against_saved_jquants_month_no_historical_revision_timestamp"
	}
	for _, s := range m.Sources {
		if ctx.Err() != nil {
			return errors.New("ORB入力検査を中断しました")
		}
		path := s.CSV
		if m.Version == 2 {
			path = s.BarsJSON
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(*input), path)
		}
		csvRaw, err := readInput(path)
		if err != nil {
			return err
		}
		var bars marketdata.Dataset
		if m.Version == 1 {
			at, _ := time.Parse(time.RFC3339, s.CapturedAt)
			bars, err = buildbars.FromCSV(ctx, csvRaw, s.Symbol, m.Environment, m.CSVLabel, at)
		} else {
			d := json.NewDecoder(bytes.NewReader(csvRaw))
			d.DisallowUnknownFields()
			if d.Decode(&bars) != nil || d.Decode(new(any)) != io.EOF {
				return errors.New("正規化済みJ-Quants分足JSONを確認してください")
			}
			err = marketdata.Validate(bars)
			if bars.Symbol != s.Symbol || bars.Environment != m.Environment || bars.Origin != "observed" || bars.Source.Format != "jquants-bulk-minute-v0.1" || bars.Source.Label != "start" {
				return errors.New("J-Quants分足の銘柄/環境/提供元を確認してください")
			}
		}
		if err != nil {
			return err
		}
		checked, err := prepareorb.CheckWithPolicy(ctx, bars, m.Date, m.Previous, m.OpeningPolicy)
		if err != nil {
			return err
		}
		provenance := orbProvenance{Symbol: s.Symbol}
		if m.Version == 1 {
			provenance.CSVFile = filepath.Base(path)
			provenance.CSVSHA256 = hash(csvRaw)
			provenance.Coverage = coverage(bars)
		} else {
			provenance.InputFile = filepath.Base(path)
			provenance.InputSHA256 = hash(csvRaw)
			provenance.InputCoverage = coverage(bars)
		}
		r.Sources = append(r.Sources, provenance)
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
		if _, err := fmt.Fprintf(out, "ORB入力: symbol=%s / date=%s / previous_complete=%d/14 / previous_unavailable=%d / target_complete=%t / calculation_available=%t\n", row.Symbol, m.Date, row.CompletePrevious, len(row.MissingPrevious), row.Target.Volume != nil && len(row.Target.MissingMinutes) == 0, row.CalculationAvailable); err != nil {
			return errors.New("ORB入力結果を出力できません")
		}
		if row.AvailablePrevious != nil {
			if _, err := fmt.Fprintf(out, "入力方針: %s / previous_available=%d/14 / source_contract_assumed=true\n", row.OpeningPolicy, *row.AvailablePrevious); err != nil {
				return errors.New("ORB入力方針を出力できません")
			}
		}
	}
	_, err = fmt.Fprintf(out, "strategy_adopted=false / trading_eligible=false / quality_promoted=false / backtest=false\n保存: %s\n実行記録: %s\n", path, runPath)
	return err
}

func relativeORBPath(manifest, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(filepath.Dir(manifest), path)
}
