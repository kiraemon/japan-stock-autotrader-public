package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/papertrade"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

func TestSessionDemoAllDirectionsAndVariantsAreReproducible(t *testing.T) {
	for _, direction := range []string{"long", "short"} {
		for _, variant := range []string{"A", "B"} {
			t.Run(direction+variant, func(t *testing.T) {
				t.Chdir(t.TempDir())
				var outputs []string
				for repeat := 0; repeat < 2; repeat++ {
					var out bytes.Buffer
					if err := run(context.Background(), []string{"session-demo", "-direction", direction, "-variant", variant}, &out); err != nil {
						t.Fatal(out.String(), err)
					}
					if strings.Count(out.String(), "passed=true") != 8 || !strings.Contains(out.String(), "forward_test=false / live_submission_capability=false") {
						t.Fatal("case count or scope", out.String())
					}
					outputs = append(outputs, out.String())
				}
				files, _ := filepath.Glob("data/research/session-simulation-*.json")
				if len(files) != 2 {
					t.Fatal("report count")
				}
				left, _ := os.ReadFile(files[0])
				right, _ := os.ReadFile(files[1])
				if !bytes.Equal(left, right) {
					t.Fatal("report differs")
				}
				var report struct {
					Forward bool `json:"forward_test"`
					Cases   []struct {
						Name   string                  `json:"name"`
						Passed bool                    `json:"passed"`
						Final  papertrade.SessionState `json:"final"`
					} `json:"cases"`
				}
				if json.Unmarshal(left, &report) != nil || report.Forward {
					t.Fatal("report")
				}
				for repeat, output := range outputs {
					var lines []string
					for line := range strings.SplitSeq(output, "\n") {
						if strings.HasPrefix(line, "case=") {
							lines = append(lines, line)
						}
					}
					if len(lines) != len(report.Cases) {
						t.Fatalf("run %d: CLI printed %d cases, JSON contains %d", repeat+1, len(lines), len(report.Cases))
					}
					for i, c := range report.Cases {
						suffix := fmt.Sprintf(" / reason=%s / open_quantity=%d", c.Final.StopReason, c.Final.Final.OpenQuantity)
						if !strings.HasPrefix(lines[i], "case="+c.Name+" / ") || !strings.HasSuffix(lines[i], suffix) {
							t.Fatalf("run %d: case %s: line %q must end with %q", repeat+1, c.Name, lines[i], suffix)
						}
					}
				}
				for _, c := range report.Cases {
					if !c.Passed {
						t.Fatal(c.Name)
					}
					state := c.Final
					if c.Name == "ordered_fresh_quotes" || c.Name == "partial_then_ttl" {
						if !state.EntryIntentAt.Equal(state.Signal.ObservedAt) || !state.EntryIntentAt.After(state.Signal.Decision.At) || !state.FirstFillAt.After(state.EntryIntentAt) || !state.ExitIntentAt.Equal(state.FirstFillAt.Add(r001.MaximumHold)) {
							t.Fatal("backdated intent/fill or holding clock", state)
						}
						wantFilled := int64(100)
						if c.Name == "partial_then_ttl" {
							wantFilled = 40
						}
						for _, order := range state.Final.Orders {
							if order.Filled != wantFilled || order.Purpose == "close" && order.Quantity != wantFilled {
								t.Fatal("exited unfilled shares", order)
							}
						}
						for _, record := range state.Final.Records {
							if record.Event.Kind == "canceled" && !record.Event.At.Equal(state.EntryIntentAt.Add(60*time.Second)) {
								t.Fatal("TTL not based on intent", record)
							}
						}
					}
					if state.Halted && (state.Final.OpenQuantity != 40 || state.Final.ActiveOrders != 1 || state.Final.RemainingQuantity != 60 || state.Final.CleanFinish) {
						t.Fatal("stop erased exposure", state)
					}
					if c.Name == "volume_filter_skip" && (len(state.Final.Orders) != 0 || state.Signal == nil || state.Signal.Decision.Accepted || state.Config.Variant != r001.B) {
						t.Fatal("B skip not preserved")
					}
				}
				manifests, _ := filepath.Glob("data/research/research-run-*.json")
				for _, file := range manifests {
					raw, _ := os.ReadFile(file)
					var m struct {
						InputFile  string `json:"input_file"`
						InputSHA   string `json:"input_sha256"`
						ReportFile string `json:"report_file"`
						ReportSHA  string `json:"report_sha256"`
					}
					if json.Unmarshal(raw, &m) != nil {
						t.Fatal("manifest")
					}
					input, _ := os.ReadFile(filepath.Join("data/research", m.InputFile))
					output, _ := os.ReadFile(filepath.Join("data/research", m.ReportFile))
					if hash(input) != m.InputSHA || hash(output) != m.ReportSHA {
						t.Fatal("saved input/output hash")
					}
					var cases []sessionCaseInput
					if json.Unmarshal(input, &cases) != nil {
						t.Fatal("saved session events")
					}
					for _, c := range cases {
						for _, action := range c.Actions {
							if len(action.RawBoard) == 0 {
								continue
							}
							var compact bytes.Buffer
							if json.Compact(&compact, action.RawBoard) != nil || action.Input.Quote == nil || hash(compact.Bytes()) != action.Input.Quote.SourceSHA256 {
								t.Fatal("normalised quote lost its saved synthetic board source")
							}
						}
					}
				}
			})
		}
	}
}

func TestSessionReversalAndHoldingTimerCreateOneExit(t *testing.T) {
	for _, direction := range []r001.Direction{r001.Long, r001.Short} {
		inputs, _ := sessionFixtures(direction, r001.A)
		input := inputs[0]
		session, _ := makeSession(input.Config)
		for _, action := range input.Actions {
			if action.Input.Kind == "minute" && action.Input.Bar.Start.Equal(marketdata.Clock(input.Config.Date, 9, 35)) {
				b := *action.Input.Bar
				b.Close = 100 * marketdata.PriceScale
				if b.Close < b.Low {
					b.Low = b.Close
				}
				if b.Close > b.High {
					b.High = b.Close
				}
				action.Input.Bar = &b
			}
			session.Step(action.At, action.Input)
		}
		state := session.Snapshot()
		count := 0
		for _, r := range state.Final.Records {
			if r.Event.Kind == "close_intent" {
				count++
			}
		}
		if count != 1 || state.ExitReason != "range_reversal" || !state.Complete || !state.Final.CleanFinish {
			t.Fatal("duplicate exit or lost reversal", state)
		}
	}
}

func TestSessionUnfilledExitAndUnknownPreserveExposure(t *testing.T) {
	inputs, _ := sessionFixtures(r001.Long, r001.A)
	input := inputs[0]
	var firstFill time.Time
	for _, action := range input.Actions {
		if action.Name == "partial_entry" {
			firstFill = action.At
		}
	}
	deadline := firstFill.Add(r001.MaximumHold + 60*time.Second)
	actions := []sessionAction{}
	for _, action := range input.Actions {
		if action.Name != "fresh_exit" {
			actions = append(actions, action)
		}
	}
	actions = append(actions, sessionAction{At: deadline, Input: papertrade.SessionInput{Kind: "tick"}})
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].At.Before(actions[j].At) })
	session, _ := makeSession(input.Config)
	for _, action := range actions {
		session.Step(action.At, action.Input)
	}
	state := session.Snapshot()
	if state.StopReason != "exit_fill_timeout" || state.Final.OpenQuantity != 100 || state.Final.ActiveOrders != 1 || state.Final.CleanFinish {
		t.Fatal("unfilled exit erased", state)
	}
	unknown, _ := makeSession(input.Config)
	for _, action := range inputs[4].Actions[:len(inputs[4].Actions)-1] {
		unknown.Step(action.At, action.Input)
	}
	unknown.Step(firstFill.Add(time.Millisecond), papertrade.SessionInput{Kind: "fault", Fault: "execution_unknown"})
	state = unknown.Snapshot()
	if state.StopReason != "execution_unknown" || state.Final.PositionKnown || !state.Final.ReconcileRequired || state.Final.OpenQuantity != 40 || state.Final.ActiveOrders != 1 {
		t.Fatal("unknown treated as flat/known", state)
	}
	count := len(state.Final.Records)
	unknown.Step(firstFill.Add(time.Second), papertrade.SessionInput{Kind: "tick"})
	if len(unknown.Snapshot().Final.Records) != count {
		t.Fatal("unknown retried")
	}
}

func TestIndependentRunnerTimerStopsSilentSource(t *testing.T) {
	inputs, _ := sessionFixtures(r001.Long, r001.A)
	session, _ := makeSession(inputs[0].Config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	input := make(chan papertrade.SessionInput) // Never produces an event.
	now := marketdata.Clock(inputs[0].Config.Date, 9, 1).Add(10*time.Second + time.Millisecond)
	stored := 0
	state, err := papertrade.RunQuoteSession(ctx, session, input, ticker.C, func() time.Time { return now }, func(s papertrade.SessionState) error { stored++; return nil })
	if err != nil || state.StopReason != "minute_arrival_timeout" || state.Observed != 0 || stored != 1 {
		t.Fatal("source blocked timer or used ticker payload", state, err, stored)
	}
}

func TestRunnerAndDirectSessionHaveIdenticalOrderedResults(t *testing.T) {
	fixtures, _ := sessionFixtures(r001.Long, r001.B)
	fixture := fixtures[0]
	direct, _ := makeSession(fixture.Config)
	running, _ := makeSession(fixture.Config)
	input := make(chan papertrade.SessionInput, len(fixture.Actions))
	for _, action := range fixture.Actions {
		direct.Step(action.At, action.Input)
		input <- action.Input
	}
	index := 0
	ticks := make(chan time.Time)
	state, err := papertrade.RunQuoteSession(context.Background(), running, input, ticks, func() time.Time { return fixture.Actions[index].At }, func(state papertrade.SessionState) error {
		index++
		// Saving code must not be able to mutate the live signal or ledger.
		if state.Signal != nil && state.Signal.Decision.Volume != nil {
			*state.Signal.Decision.Volume.SignalVolume = 0
		}
		if len(state.Final.Records) > 0 {
			state.Final.Records[0].Event.Quantity = 0
		}
		return nil
	})
	left, _ := json.Marshal(state)
	right, _ := json.Marshal(direct.Snapshot())
	if err != nil || index != len(fixture.Actions) || !bytes.Equal(left, right) {
		t.Fatal("channel runner changed ordered semantics or snapshots alias live state", err)
	}
}

func TestRunnerQueuedMinuteCannotRescueExpiredDeadline(t *testing.T) {
	inputs, _ := sessionFixtures(r001.Long, r001.A)
	session, _ := makeSession(inputs[0].Config)
	input := make(chan papertrade.SessionInput, 1)
	input <- inputs[0].Actions[0].Input
	ticks := make(chan time.Time, 1)
	ticks <- inputs[0].Actions[0].At
	now := marketdata.Clock(inputs[0].Config.Date, 9, 1).Add(10*time.Second + time.Millisecond)
	state, err := papertrade.RunQuoteSession(context.Background(), session, input, ticks, func() time.Time { return now }, func(papertrade.SessionState) error { return nil })
	if err != nil || state.Observed != 0 || state.StopReason != "minute_arrival_timeout" {
		t.Fatal("queued time used as current time", state, err)
	}
}

func TestRunnerCancellationAndFailedPersistenceKeepPosition(t *testing.T) {
	for _, kind := range []string{"cancel", "storage", "source_closed", "timer_closed"} {
		t.Run(kind, func(t *testing.T) {
			inputs, _ := sessionFixtures(r001.Long, r001.A)
			inputCase := inputs[4]
			session, _ := makeSession(inputCase.Config)
			for _, action := range inputCase.Actions[:len(inputCase.Actions)-1] {
				session.Step(action.At, action.Input)
			}
			now := session.Snapshot().FirstFillAt.Add(time.Millisecond)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			input := make(chan papertrade.SessionInput)
			ticks := make(chan time.Time, 1)
			want := "storage_failure"
			switch kind {
			case "cancel":
				cancel()
				want = "context_canceled"
			case "source_closed":
				close(input)
				want = "source_disconnected"
			case "timer_closed":
				close(ticks)
				want = "timer_disconnected"
			default:
				ticks <- now.Add(-time.Minute)
			}
			state, err := papertrade.RunQuoteSession(ctx, session, input, ticks, func() time.Time { return now }, func(papertrade.SessionState) error {
				if kind == "storage" {
					return errors.New("synthetic sensitive error detail")
				}
				return nil
			})
			if state.StopReason != want || state.Final.OpenQuantity != 40 || state.Final.ActiveOrders != 1 || state.Final.RemainingQuantity != 60 || state.Final.CleanFinish {
				t.Fatal(state, err)
			}
			if kind == "storage" && (err == nil || strings.Contains(err.Error(), "sensitive")) {
				t.Fatal("raw storage error exposed", err)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation not reported")
			}
		})
	}
}

func TestSessionDemoRejectsExternalInputsAndObservedConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"session-demo", "-input", "must-not-read.json"}, {"session-demo", "-reference", "must-not-read.json"}, {"session-demo", "-symbol", "7203@1"}, {"session-demo", "-captured-at", "2026-10-16T09:00:00+09:00"},
	} {
		if run(context.Background(), args, &bytes.Buffer{}) == nil {
			t.Fatal("external input accepted")
		}
	}
	fixtures, _ := sessionFixtures(r001.Long, r001.A)
	config := fixtures[0].Config
	config.Origin, config.Symbol, config.Environment = "observed", "7203@1", "production"
	if _, err := makeSession(config); err == nil {
		t.Fatal("observed session accepted")
	}
}
