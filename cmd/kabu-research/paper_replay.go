package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/papertrade"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

func paperReplay(ctx context.Context, out io.Writer, direction r001.Direction, variant r001.Variant) error {
	dataset := backtest.Synthetic(direction)
	inputPath, inputRaw, err := save("synthetic-bars", dataset)
	if err != nil {
		return err
	}
	models := []papertrade.FillModel{papertrade.FullFill, papertrade.PartialFill, papertrade.Unfilled, papertrade.UnknownFill}
	reports := []papertrade.ReplayReport{}
	for _, model := range models {
		r, err := papertrade.Replay(ctx, dataset, direction, variant, model)
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	path, raw, err := save("paper-replay", reports)
	if err != nil {
		return err
	}
	settings := struct {
		Version                                          string
		Direction                                        r001.Direction
		Variant                                          r001.Variant
		Models                                           []papertrade.FillModel
		SyntheticEntryQuantity, SyntheticPartialQuantity int64
		SyntheticEntryTTLSeconds                         int
	}{papertrade.ReplayVersion, direction, variant, models, papertrade.SyntheticEntryQuantity, papertrade.SyntheticPartialQuantity, papertrade.SyntheticEntryTTLSeconds}
	manifest, err := recordRun("paper-replay", filepath.Base(inputPath), inputRaw, dataset.Source.SHA256, path, raw, settings)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "戦略台帳再生: synthetic=true / forward_test=false / live_submission_capability=false"); err != nil {
		return err
	}
	for _, report := range reports {
		for _, day := range report.Days {
			if _, err := fmt.Fprintf(out, "model=%s / status=%s / position_known=%t / active_orders=%d / clean_finish=%t / reason=%s\n", report.Model, day.Status, day.Final.PositionKnown, day.Final.ActiveOrders, day.Final.CleanFinish, day.Reason); err != nil {
				return err
			}
		}
	}
	_, err = fmt.Fprintf(out, "保存: %s\n実行記録: %s\n", path, manifest)
	return err
}
