package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/backtest"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/papertrade"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/streamsignals"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

type sessionGate struct{ feed *streamsignals.Feed }

func (g sessionGate) Offer(now, received time.Time, bar marketdata.Bar) papertrade.MinuteDelivery {
	before := len(g.feed.Snapshot().Signals)
	out := g.feed.Offer(now, streamsignals.Observation{ReceivedAt: received, Bar: bar})
	state := g.feed.Snapshot()
	d := papertrade.MinuteDelivery{Accepted: out.Accepted, Duplicate: out.Duplicate, Complete: state.Complete, Reason: out.Reason}
	if len(state.Signals) > before {
		decision := state.Signals[len(state.Signals)-1].Decision
		d.Decision = &decision
	}
	return d
}

func (g sessionGate) Tick(now time.Time) papertrade.MinuteDelivery {
	out := g.feed.Tick(now)
	return papertrade.MinuteDelivery{Accepted: out.Accepted, Reason: out.Reason, Complete: g.feed.Snapshot().Complete}
}

func makeSession(config papertrade.SessionConfig) (*papertrade.QuoteSession, error) {
	gate, err := streamsignals.New(streamsignals.Config{Date: config.Date, Symbol: config.Symbol, Origin: config.Origin, Direction: config.Direction, Variant: config.Variant})
	if err != nil {
		return nil, err
	}
	return papertrade.NewQuoteSession(config, sessionGate{gate})
}

type sessionAction struct {
	Name              string                  `json:"name"`
	At                time.Time               `json:"observed_at"`
	Input             papertrade.SessionInput `json:"input"`
	RawBoard          json.RawMessage         `json:"synthetic_board,omitempty"`
	ExpectedFill      *bool                   `json:"expected_fill,omitempty"`
	ExpectedReason    string                  `json:"expected_fill_reason,omitempty"`
	ExpectedDuplicate bool                    `json:"expected_duplicate,omitempty"`
}

type sessionCaseInput struct {
	Name             string                   `json:"name"`
	Config           papertrade.SessionConfig `json:"config"`
	Actions          []sessionAction          `json:"actions"`
	ExpectedStop     string                   `json:"expected_stop_reason"`
	ExpectedPosition int64                    `json:"expected_position"`
	ExpectedClean    bool                     `json:"expected_clean_finish"`
	ExpectedComplete bool                     `json:"expected_complete"`
}

func sessionFixtures(direction r001.Direction, variant r001.Variant) ([]sessionCaseInput, error) {
	dataset := backtest.Synthetic(direction)
	day := dataset.Days[0].Date
	config := papertrade.SessionConfig{Date: day, Symbol: "0000@1", Environment: "test", Origin: "synthetic", Direction: direction, Variant: variant}
	entry := marketdata.Clock(day, 9, 16).Add(streamsignals.SyntheticCloseLag)
	firstFill := entry.Add(1100 * time.Millisecond)
	minutes := func(filtered bool) []sessionAction {
		actions := []sessionAction{}
		for i, b := range dataset.Days[0].Bars {
			if i == 15 && !filtered {
				quantity := int64(150) // B's exact boundary, an explicit synthetic fixture.
				b.Volume = &quantity
			}
			bar := b
			at := bar.Start.Add(time.Minute + streamsignals.SyntheticCloseLag)
			actions = append(actions, sessionAction{Name: "minute", At: at, Input: papertrade.SessionInput{Kind: "minute", ReceivedAt: at, Bar: &bar}})
		}
		return actions
	}
	quote := func(name string, marketAt, now time.Time, quantity int64, expectedReason string) (sessionAction, error) {
		raw := syntheticBoard(marketAt, quantity)
		received := marketAt.Add(100 * time.Millisecond)
		q, err := marketdata.KabuBoardQuote(raw, "0000@1", "test", received)
		if err != nil {
			return sessionAction{}, err
		}
		q.Origin = "synthetic" // Only generated, built-in boards take this path.
		filled := expectedReason == ""
		return sessionAction{Name: name, At: now, Input: papertrade.SessionInput{Kind: "quote", Quote: &q}, RawBoard: raw, ExpectedFill: &filled, ExpectedReason: expectedReason}, nil
	}
	partial, err := quote("partial_entry", entry.Add(time.Second), firstFill, 40, "")
	if err != nil {
		return nil, err
	}
	old, _ := quote("pre_intent_quote", entry.Add(-time.Second), entry.Add(500*time.Millisecond), 100, "quote_not_after_intent")
	duplicate, _ := quote("duplicate_quote", entry.Add(time.Second), entry.Add(1200*time.Millisecond), 40, "quote_already_consumed")
	remaining, _ := quote("remaining_entry", entry.Add(2*time.Second), entry.Add(2100*time.Millisecond), 100, "")
	exitTime := firstFill.Add(r001.MaximumHold)
	exitQuote, _ := quote("fresh_exit", exitTime.Add(900*time.Millisecond), exitTime.Add(time.Second), 100, "")
	// Sorting is only fixture construction. Runtime input is processed in arrival
	// order and an observed clock reversal is never silently repaired.
	ordered := func(actions []sessionAction) []sessionAction {
		sort.SliceStable(actions, func(i, j int) bool { return actions[i].At.Before(actions[j].At) })
		return actions
	}
	base := minutes(false)
	duplicateMinute := base[15]
	duplicateMinute.Name, duplicateMinute.At = "duplicate_minute", entry.Add(200*time.Millisecond)
	duplicateMinute.ExpectedDuplicate = true
	normal := ordered(append(minutes(false), old, partial, duplicate, remaining, duplicateMinute, sessionAction{Name: "holding_timer", At: exitTime, Input: papertrade.SessionInput{Kind: "tick"}}, exitQuote))
	partialTTL := ordered(append(minutes(false), partial, sessionAction{Name: "holding_timer", At: exitTime, Input: papertrade.SessionInput{Kind: "tick"}}, exitQuote))
	prefix := ordered(append(append([]sessionAction{}, base[:16]...), partial))
	withTerminal := func(action sessionAction) []sessionAction {
		return append(append([]sessionAction{}, prefix...), action)
	}
	observed, _ := quote("observed_quote", entry.Add(2*time.Second), entry.Add(2100*time.Millisecond), 100, "")
	observed.Input.Quote.Origin, observed.ExpectedFill = "observed", nil
	filtered := config
	filtered.Variant = r001.B
	return []sessionCaseInput{
		{"ordered_fresh_quotes", config, normal, "", 0, true, true},
		{"partial_then_ttl", config, partialTTL, "", 0, true, true},
		{"unfilled_then_ttl", config, minutes(false), "", 0, true, true},
		{"silence_with_exposure", config, withTerminal(sessionAction{Name: "silence_timer", At: entry.Add(time.Minute + 8*time.Second + time.Millisecond), Input: papertrade.SessionInput{Kind: "tick"}}), "minute_arrival_timeout", 40, false, false},
		{"source_failure_with_exposure", config, withTerminal(sessionAction{Name: "source_failure", At: entry.Add(2 * time.Second), Input: papertrade.SessionInput{Kind: "fault", Fault: "source_disconnected"}}), "source_disconnected", 40, false, false},
		{"clock_reversed_with_exposure", config, withTerminal(sessionAction{Name: "reversed_clock", At: entry.Add(900 * time.Millisecond), Input: papertrade.SessionInput{Kind: "tick"}}), "observation_clock_reversed_or_invalid", 40, false, false},
		{"observed_quote_blocked", config, withTerminal(observed), "observed_quote_execution_pending", 40, false, false},
		{"volume_filter_skip", filtered, minutes(true), "", 0, true, true},
	}, nil
}

func sessionDemo(ctx context.Context, out io.Writer, direction r001.Direction, variant r001.Variant) error {
	inputs, err := sessionFixtures(direction, variant)
	if err != nil {
		return err
	}
	type result struct {
		Name     string                      `json:"name"`
		Passed   bool                        `json:"passed"`
		Outcomes []papertrade.SessionOutcome `json:"outcomes"`
		Final    papertrade.SessionState     `json:"final"`
	}
	report := struct {
		Version                  string   `json:"version"`
		ClockModel               string   `json:"clock_model"`
		ForwardTest              bool     `json:"forward_test"`
		LiveSubmissionCapability bool     `json:"live_submission_capability"`
		Cases                    []result `json:"cases"`
	}{Version: papertrade.SessionVersion, ClockModel: "synthetic_observation_events", Cases: []result{}}
	passed := true
	for _, input := range inputs {
		session, err := makeSession(input.Config)
		if err != nil {
			return err
		}
		c := result{Name: input.Name, Passed: true, Outcomes: []papertrade.SessionOutcome{}}
		for _, action := range input.Actions {
			if ctx.Err() != nil {
				return errors.New("時刻順セッションの模擬試験を中断しました")
			}
			outcome := session.Step(action.At, action.Input)
			if action.ExpectedFill != nil {
				c.Passed = c.Passed && outcome.Fill != nil && outcome.Fill.Filled == *action.ExpectedFill && outcome.Fill.Reason == action.ExpectedReason
			}
			if action.ExpectedDuplicate {
				c.Passed = c.Passed && outcome.Duplicate
			}
			c.Outcomes = append(c.Outcomes, outcome)
		}
		c.Final = session.Snapshot()
		c.Passed = c.Passed && c.Final.StopReason == input.ExpectedStop && c.Final.Halted == (input.ExpectedStop != "") && c.Final.Final.OpenQuantity == input.ExpectedPosition && c.Final.Final.CleanFinish == input.ExpectedClean && c.Final.Complete == input.ExpectedComplete
		report.Cases = append(report.Cases, c)
		passed = passed && c.Passed
	}
	inputPath, inputRaw, err := save("synthetic-session-events", inputs)
	if err != nil {
		return err
	}
	path, raw, err := save("session-simulation", report)
	if err != nil {
		return err
	}
	settings := struct {
		Session, Gate, Execution, Rule                              string
		Direction                                                   r001.Direction
		Variant                                                     r001.Variant
		Quantity, TTLSeconds                                        int64
		CloseLagSeconds, ArrivalDeadlineSeconds, QuoteMaxAgeSeconds int64
		MaximumHoldSeconds                                          int64
	}{Session: papertrade.SessionVersion, Gate: streamsignals.Version, Execution: papertrade.QuoteExecutionVersion, Rule: r001.VersionFor(direction, variant), Direction: direction, Variant: variant, Quantity: papertrade.SyntheticEntryQuantity, TTLSeconds: papertrade.SyntheticEntryTTLSeconds, CloseLagSeconds: int64(streamsignals.SyntheticCloseLag.Seconds()), ArrivalDeadlineSeconds: int64(streamsignals.SyntheticArrivalDeadline.Seconds()), QuoteMaxAgeSeconds: int64(papertrade.SyntheticQuoteMaxAge.Seconds()), MaximumHoldSeconds: int64(r001.MaximumHold.Seconds())}
	manifest, err := recordRun("session-demo", filepath.Base(inputPath), inputRaw, hash(inputRaw), path, raw, settings)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "時刻順セッション: synthetic=true / forward_test=false / live_submission_capability=false"); err != nil {
		return err
	}
	for _, c := range report.Cases {
		if _, err := fmt.Fprintf(out, "case=%s / passed=%t / complete=%t / clean_finish=%t / reason=%s\n", c.Name, c.Passed, c.Final.Complete, c.Final.Final.CleanFinish, c.Final.StopReason); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "保存: %s\n実行記録: %s\n", path, manifest); err != nil {
		return err
	}
	if !passed {
		return errors.New("時刻順セッションの期待結果と一致しません")
	}
	return nil
}
