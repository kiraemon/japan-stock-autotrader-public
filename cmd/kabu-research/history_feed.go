package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/streambars"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

// historyFeedCheck replays two raw archives in the explicit input/reference
// order. finished_at is only a historical observation-time model; this command
// never describes past market data as freshly received live input.
func historyFeedCheck(ctx context.Context, out io.Writer, input, reference, symbol, environment string) error {
	if input == "" || reference == "" || symbol == "" {
		return errors.New("history-feed-checkにはinput・reference・symbolを指定してください（取得順の2履歴）")
	}
	type frame struct {
		File       string             `json:"source_file"`
		SHA256     string             `json:"source_sha256"`
		ReceivedAt time.Time          `json:"history_received_at"`
		ObservedAt time.Time          `json:"modeled_observed_at"`
		Outcome    streambars.Outcome `json:"outcome"`
	}
	report := struct {
		Version           string           `json:"version"`
		ClockModel        string           `json:"clock_model"`
		ForwardTest       bool             `json:"forward_test"`
		ExecutionIncluded bool             `json:"execution_included"`
		Frames            []frame          `json:"frames"`
		Final             streambars.State `json:"final"`
	}{Version: streambars.Version, ClockModel: "archive_finished_at_replay_not_live", Frames: []frame{}}
	var feed *streambars.Feed
	var firstRaw []byte
	for _, path := range []string{input, reference} {
		if ctx.Err() != nil {
			return errors.New("履歴分足検査を中断しました")
		}
		raw, err := readInput(path)
		if err != nil {
			return err
		}
		dataset, err := buildbars.FromHistory(ctx, raw)
		if err != nil {
			return err
		}
		var metadata struct {
			Finished time.Time `json:"finished_at"`
		}
		if json.Unmarshal(raw, &metadata) != nil || metadata.Finished.IsZero() {
			return errors.New("履歴の完了時刻を確認してください")
		}
		if feed == nil {
			feed, err = streambars.New(streambars.Config{Date: marketdata.Date(dataset.Source.ReceivedAt), Symbol: symbol, Environment: environment})
			if err != nil {
				return err
			}
			firstRaw = raw
		}
		result := feed.Offer(metadata.Finished, dataset)
		report.Frames = append(report.Frames, frame{filepath.Base(path), hash(raw), dataset.Source.ReceivedAt, metadata.Finished, result})
	}
	report.Final = feed.Snapshot()
	path, raw, err := save("history-minute-audit", report)
	if err != nil {
		return err
	}
	settings := struct {
		Version, Converter, ClockModel          string
		Config                                  streambars.Config
		ReferenceFile, ReferenceSHA256          string
		CloseLagSeconds, ArrivalDeadlineSeconds int
	}{streambars.Version, buildbars.Version, report.ClockModel, report.Final.Config, filepath.Base(reference), report.Frames[1].SHA256, int(streambars.CloseLag.Seconds()), int(streambars.ArrivalDeadline.Seconds())}
	manifest, err := recordRun("history-feed-check", filepath.Base(input), firstRaw, hash(firstRaw), path, raw, settings)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "履歴分足検査: clock=archive_finish_replay / candidates=%d / late=%d / halted=%t / reason=%s\nforward_test=false / live_ready=false / execution_included=false\n保存: %s\n実行記録: %s\n", report.Final.Candidates, report.Final.Late, report.Final.Halted, report.Final.StopReason, path, manifest)
	return err
}
