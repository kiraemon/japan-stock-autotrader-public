package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/papertrade"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

type quoteAction struct {
	Name           string            `json:"name"`
	At             time.Time         `json:"observed_at"`
	Event          *papertrade.Event `json:"event,omitempty"`
	Board          json.RawMessage   `json:"synthetic_board,omitempty"`
	OrderID        string            `json:"order_id,omitempty"`
	ReceivedAt     time.Time         `json:"received_at,omitempty"`
	ExpectedReason string            `json:"expected_reason,omitempty"`
}

func syntheticBoard(at time.Time, quantity int64) json.RawMessage {
	type level struct {
		Time       time.Time
		Sign       string
		Price, Qty int64
	}
	raw, _ := json.Marshal(struct {
		Symbol             string
		Exchange           int
		BidPrice, AskPrice int64
		Sell1, Buy1        level
	}{"0000", 1, 101, 99, level{at, "0101", 101, quantity}, level{at, "0101", 99, quantity}})
	return raw
}

func quoteDemo(ctx context.Context, out io.Writer, direction r001.Direction) error {
	at := marketdata.Clock("2026-10-16", 9, 16).Add(2 * time.Second)
	event := func(id, kind, order string, when time.Time, quantity int64) quoteAction {
		return quoteAction{Name: kind, At: when, Event: &papertrade.Event{ID: id, At: when, Kind: kind, OrderID: order, Quantity: quantity}}
	}
	quote := func(name, order string, marketAt, received, now time.Time, quantity int64, expected string) quoteAction {
		return quoteAction{Name: name, At: now, Board: syntheticBoard(marketAt, quantity), OrderID: order, ReceivedAt: received, ExpectedReason: expected}
	}
	actions := []quoteAction{
		event("entry", "open_intent", "entry", at, 100), event("entry-ack", "accepted", "entry", at, 0),
		quote("partial_entry", "entry", at.Add(time.Second), at.Add(1100*time.Millisecond), at.Add(1100*time.Millisecond), 40, ""),
		quote("duplicate_book", "entry", at.Add(time.Second), at.Add(1200*time.Millisecond), at.Add(1200*time.Millisecond), 40, "quote_already_consumed"),
		quote("remaining_entry", "entry", at.Add(2*time.Second), at.Add(2100*time.Millisecond), at.Add(2100*time.Millisecond), 100, ""),
		event("exit", "close_intent", "exit", at.Add(3*time.Second), 100), event("exit-ack", "accepted", "exit", at.Add(3*time.Second), 0),
		quote("old_quote_exit", "exit", at.Add(2*time.Second), at.Add(2100*time.Millisecond), at.Add(3100*time.Millisecond), 100, "quote_not_after_intent"),
		quote("fresh_exit", "exit", at.Add(4*time.Second), at.Add(4100*time.Millisecond), at.Add(4100*time.Millisecond), 100, ""),
	}
	type step struct {
		Name   string               `json:"name"`
		Passed bool                 `json:"passed"`
		Fill   papertrade.QuoteFill `json:"fill"`
	}
	report := struct {
		Version        string              `json:"version"`
		Direction      r001.Direction      `json:"direction"`
		ForwardTest    bool                `json:"forward_test"`
		LiveSubmission bool                `json:"live_submission_capability"`
		Steps          []step              `json:"steps"`
		Final          papertrade.Snapshot `json:"final"`
	}{Version: papertrade.QuoteExecutionVersion, Direction: direction, Steps: []step{}}
	s, _ := papertrade.New(direction)
	execution := papertrade.NewQuoteExecution(s)
	passed := true
	for _, action := range actions {
		if ctx.Err() != nil {
			return errors.New("新気配の模擬試験を中断しました")
		}
		if action.Event != nil {
			if !s.Apply(*action.Event).Accepted {
				return errors.New("合成注文イベントが拒否されました")
			}
			continue
		}
		q, err := marketdata.KabuBoardQuote(action.Board, "0000@1", "test", action.ReceivedAt)
		if err != nil {
			return err
		}
		// Only this built-in fixture is synthetic. No external file can set this.
		q.Origin = "synthetic"
		fill := execution.TryFill(action.OrderID, action.At, q)
		ok := fill.Reason == action.ExpectedReason && fill.Filled == (action.ExpectedReason == "")
		report.Steps = append(report.Steps, step{action.Name, ok, fill})
		passed = passed && ok
	}
	report.Final = s.Snapshot()
	passed = passed && report.Final.CleanFinish
	inputPath, inputRaw, err := save("synthetic-quote-events", struct {
		Direction r001.Direction `json:"direction"`
		Actions   []quoteAction  `json:"actions"`
	}{direction, actions})
	if err != nil {
		return err
	}
	path, raw, err := save("quote-simulation", report)
	if err != nil {
		return err
	}
	settings := struct {
		Model, Normalizer  string
		Direction          r001.Direction
		MaxAgeMilliseconds int64
	}{papertrade.QuoteExecutionVersion, marketdata.QuoteVersion, direction, papertrade.SyntheticQuoteMaxAge.Milliseconds()}
	manifest, err := recordRun("quote-demo", filepath.Base(inputPath), inputRaw, hash(inputRaw), path, raw, settings)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintln(out, "新気配の仮想約定: synthetic=true / forward_test=false / live_submission_capability=false"); err != nil {
		return err
	}
	for _, step := range report.Steps {
		if _, err = fmt.Fprintf(out, "step=%s / passed=%t / filled=%t / reason=%s\n", step.Name, step.Passed, step.Fill.Filled, step.Fill.Reason); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(out, "clean_finish=%t\n保存: %s\n実行記録: %s\n", report.Final.CleanFinish, path, manifest); err != nil {
		return err
	}
	if !passed {
		return errors.New("新気配モデルの期待結果と一致しません")
	}
	return nil
}

func quoteCheck(ctx context.Context, out io.Writer, input, symbol, environment string) error {
	if input == "" || symbol == "" {
		return errors.New("quote-checkにはinputとsymbolを指定してください")
	}
	if ctx.Err() != nil {
		return errors.New("気配検査を中断しました")
	}
	raw, err := readInput(input)
	if err != nil {
		return err
	}
	var snapshot struct {
		Version     int    `json:"schema_version"`
		Environment string `json:"environment"`
		Complete    bool   `json:"complete"`
		Records     []struct {
			Endpoint   string          `json:"endpoint"`
			ReceivedAt time.Time       `json:"received_at"`
			Data       json.RawMessage `json:"data"`
			Error      string          `json:"error"`
		} `json:"records"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != 1 || !snapshot.Complete || snapshot.Environment != environment {
		return errors.New("完了した同環境のスナップショットを指定してください")
	}
	var q marketdata.Quote
	found := 0
	for _, record := range snapshot.Records {
		if record.Endpoint != "/board/"+symbol {
			continue
		}
		found++
		if record.Error != "" {
			return errors.New("板の取得エラーを含む記録です")
		}
		q, err = marketdata.KabuBoardQuote(record.Data, symbol, environment, record.ReceivedAt)
		if err != nil {
			return err
		}
	}
	if found != 1 {
		return errors.New("指定銘柄の板記録が一意ではありません")
	}
	now := time.Now().UTC()
	fresh := len(q.Issues) == 0
	for _, stamp := range []time.Time{q.ReceivedAt, q.Bid.Time, q.Ask.Time} {
		fresh = fresh && !stamp.IsZero() && !stamp.After(now) && now.Sub(stamp) <= papertrade.SyntheticQuoteMaxAge
	}
	report := struct {
		Version           string           `json:"version"`
		ObservedAt        time.Time        `json:"observed_at"`
		Quote             marketdata.Quote `json:"quote"`
		Fresh             bool             `json:"fresh_at_audit"`
		ExecutionIncluded bool             `json:"execution_included"`
	}{marketdata.QuoteVersion, now, q, fresh, false}
	path, output, err := save("quote-audit", report)
	if err != nil {
		return err
	}
	manifest, err := recordRun("quote-check", filepath.Base(input), raw, q.SourceSHA256, path, output, struct {
		Normalizer, Symbol, Environment string
		MaxAgeMilliseconds              int64
	}{marketdata.QuoteVersion, symbol, environment, papertrade.SyntheticQuoteMaxAge.Milliseconds()})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "気配検査: observed=true / issues=%d / fresh=%t / execution_included=false\n保存: %s\n実行記録: %s\n", len(q.Issues), fresh, path, manifest)
	return err
}
