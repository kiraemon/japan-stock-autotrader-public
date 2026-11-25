package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/backtest"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/streamsignals"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

func feedDemo(ctx context.Context, out io.Writer, direction r001.Direction, variant r001.Variant) error {
	dataset := backtest.Synthetic(direction)
	inputs, report, err := streamsignals.Demo(ctx, dataset, direction, variant)
	if err != nil {
		return err
	}
	inputPath, inputRaw, err := save("synthetic-feed-events", inputs)
	if err != nil {
		return err
	}
	path, raw, err := save("feed-simulation", report)
	if err != nil {
		return err
	}
	settings := struct {
		Version                                 string
		Direction                               r001.Direction
		Variant                                 r001.Variant
		CloseLagSeconds, ArrivalDeadlineSeconds int
	}{streamsignals.Version, direction, variant, int(streamsignals.SyntheticCloseLag.Seconds()), int(streamsignals.SyntheticArrivalDeadline.Seconds())}
	manifest, err := recordRun("feed-demo", filepath.Base(inputPath), inputRaw, dataset.Source.SHA256, path, raw, settings)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintln(out, "確定足入力の模擬試験: synthetic=true / forward_test=false / execution_included=false"); err != nil {
		return err
	}
	passed := true
	for _, c := range report.Cases {
		passed = passed && c.Passed
		if _, err = fmt.Fprintf(out, "case=%s / passed=%t / observed=%d / halted=%t / reason=%s\n", c.Name, c.Passed, c.Final.Observed, c.Final.Halted, c.Final.StopReason); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(out, "保存: %s\n実行記録: %s\n", path, manifest); err != nil {
		return err
	}
	if !passed {
		return errors.New("確定足入力の模擬試験で期待状態と一致しません")
	}
	return nil
}
