package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

type diagnosticOptions struct {
	Input, Reference, Symbol, Environment, Label, Captured string
	Direction                                              r001.Direction
	Variant                                                r001.Variant
}

func diagnose(ctx context.Context, out io.Writer, opts diagnosticOptions) error {
	if opts.Input == "" || opts.Reference == "" || opts.Symbol == "" || opts.Label != "start" {
		return errors.New("diagnoseには元CSV input・元歩み値JSON reference・symbol・csv-label startが必要です")
	}
	at, err := time.Parse(time.RFC3339, opts.Captured)
	if err != nil {
		return errors.New("CSV取得日時をRFC3339で指定してください")
	}
	raw, err := readInput(opts.Input)
	if err != nil {
		return err
	}
	refRaw, err := readInput(opts.Reference)
	if err != nil {
		return err
	}
	csv, err := buildbars.FromCSV(ctx, raw, opts.Symbol, opts.Environment, opts.Label, at)
	if err != nil {
		return err
	}
	history, err := buildbars.FromHistory(ctx, refRaw)
	if err != nil {
		return err
	}
	report, err := backtest.DiagnoseVariant(ctx, csv, history, opts.Direction, opts.Variant)
	if err != nil {
		return err
	}
	path, outputRaw, err := save("diagnostic", report)
	if err != nil {
		return err
	}
	settings := struct {
		Rule, Model, Diagnostic, Converter, Symbol, Environment, Label, Captured string
		Direction                                                                r001.Direction
		Variant                                                                  r001.Variant
		ReferenceFile, ReferenceSHA256                                           string
		Costs                                                                    []int64
	}{report.Rule, report.Model, backtest.DiagnosticVersion, buildbars.Version, opts.Symbol, opts.Environment, opts.Label, opts.Captured, opts.Direction, opts.Variant, filepath.Base(opts.Reference), hash(refRaw), []int64{0, 5, 10, 20}}
	manifest, err := recordRun("diagnose", filepath.Base(opts.Input), raw, csv.Source.SHA256, path, outputRaw, settings)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "研究診断: scope=%s / strategy=%s / days=%d\n", report.Scope, report.Rule, len(report.Days)); err != nil {
		return errors.New("診断結果を出力できません")
	}
	for i, day := range report.Days {
		check := report.Diagnostic.Days[i]
		if _, err := fmt.Fprintf(out, "date=%s / status=%s / crosschecked=%d/%d / eligible=%t / reasons=%v\n", day.Date, day.Status, check.Matched, check.Required, check.Eligible, day.Reasons); err != nil {
			return errors.New("診断結果を出力できません")
		}
	}
	for _, cost := range report.Costs {
		mean := "unavailable"
		if cost.MeanBPS != nil {
			mean = *cost.MeanBPS
		}
		if _, err := fmt.Fprintf(out, "round_trip_bps=%d / trades=%d / winners=%d / mean_net_return_bps=%s\n", cost.RoundTripBPS, cost.Trades, cost.Winners, mean); err != nil {
			return errors.New("診断結果を出力できません")
		}
	}
	_, err = fmt.Fprintf(out, "保存: %s\n実行記録: %s\nstrategy_adopted=false / trading_status_unverified / execution_assumed / short_availability_unverified\n", path, manifest)
	return err
}
