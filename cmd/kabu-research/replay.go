package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/replaysignals"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func replay(ctx context.Context, out io.Writer, input string, raw []byte, dataset marketdata.Dataset, direction r001.Direction, variant r001.Variant) error {
	name := filepath.Base(input)
	if input == "" {
		path, saved, err := save("synthetic-bars", dataset)
		if err != nil {
			return err
		}
		name = filepath.Base(path)
		raw = saved
	}
	report, err := replaysignals.Run(ctx, dataset, direction, variant)
	if err != nil {
		return err
	}
	path, output, err := save("signal-replay", report)
	if err != nil {
		return err
	}
	settings := struct {
		Replay, Rule string
		Direction    r001.Direction
		Variant      r001.Variant
	}{replaysignals.Version, report.Rule, direction, variant}
	manifest, err := recordRun("replay", name, raw, dataset.Source.SHA256, path, output, settings)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "シグナル再生: origin=%s / strategy=%s / forward_test=false / execution_included=false\n", report.Origin, report.Rule); err != nil {
		return errors.New("再生結果を出力できません")
	}
	for _, day := range report.Days {
		if _, err := fmt.Fprintf(out, "date=%s / status=%s / observed_closed_minutes=%d / signal_candidates=%d / reasons=%v\n", day.Date, day.Status, day.Observed, len(day.Events), day.Reasons); err != nil {
			return errors.New("再生結果を出力できません")
		}
		for _, event := range day.Events {
			if _, err := fmt.Fprintf(out, "signal_at=%s / accepted=%t / reason=%s\n", event.Decision.At.Format("2006-01-02T15:04:05Z07:00"), event.Decision.Accepted, event.Decision.Reason); err != nil {
				return errors.New("再生結果を出力できません")
			}
		}
	}
	_, err = fmt.Fprintf(out, "保存: %s\n実行記録: %s\n", path, manifest)
	return err
}
