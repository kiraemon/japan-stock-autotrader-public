package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/papertrade"
)

func paperDemo(ctx context.Context, out io.Writer) error {
	report, err := papertrade.Demo(ctx)
	if err != nil {
		return err
	}
	type scenarioInput struct {
		Name          string             `json:"name"`
		Direction     string             `json:"direction"`
		Events        []papertrade.Event `json:"synthetic_events"`
		ExpectedClean bool               `json:"expected_clean_finish"`
	}
	inputs := []scenarioInput{}
	for _, c := range report.Cases {
		input := scenarioInput{Name: c.Name, Direction: string(c.Final.Direction), Events: []papertrade.Event{}, ExpectedClean: c.ExpectedCleanFinish}
		for _, record := range c.Final.Records {
			input.Events = append(input.Events, record.Event)
		}
		inputs = append(inputs, input)
	}
	inputPath, inputRaw, err := save("synthetic-ledger-events", inputs)
	if err != nil {
		return err
	}
	path, raw, err := save("paper-simulation", report)
	if err != nil {
		return err
	}
	settings := struct{ Version, Mode string }{papertrade.Version, report.Mode}
	// A built-in scenario suite is the input, not a market/credential file.
	manifest, err := recordRun("paper-demo", filepath.Base(inputPath), inputRaw, hash(inputRaw), path, raw, settings)
	if err != nil {
		return err
	}
	passed := 0
	for _, c := range report.Cases {
		if c.Passed {
			passed++
		}
		if _, err := fmt.Fprintf(out, "case=%s / passed=%t / position_known=%t / active_virtual_orders=%d / expected_clean_finish=%t / clean_finish=%t / checkpoint_verified=%t\n", c.Name, c.Passed, c.Final.PositionKnown, c.Final.ActiveOrders, c.ExpectedCleanFinish, c.Final.CleanFinish, c.CheckpointVerified); err != nil {
			return errors.New("合成台帳の結果を出力できません")
		}
	}
	if _, err := fmt.Fprintf(out, "合成台帳: mode=%s / cases=%d / passed=%d / forward_test=false / live_submission_capability=false\n保存: %s\n実行記録: %s\n", report.Mode, len(report.Cases), passed, path, manifest); err != nil {
		return errors.New("合成台帳の結果を出力できません")
	}
	if passed != len(report.Cases) {
		return errors.New("合成台帳の期待状態と結果が一致しません。保存した模擬結果を確認してください")
	}
	return nil
}
