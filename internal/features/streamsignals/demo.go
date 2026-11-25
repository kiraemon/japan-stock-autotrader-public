package streamsignals

import (
	"context"
	"errors"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

type Action struct {
	At          time.Time    `json:"observed_at"`
	Kind        string       `json:"kind"`
	Observation *Observation `json:"observation,omitempty"`
	Fault       string       `json:"fault,omitempty"`
}

type DemoInput struct {
	Name             string   `json:"name"`
	Config           Config   `json:"config"`
	Actions          []Action `json:"actions"`
	ExpectedStop     string   `json:"expected_stop_reason"`
	ExpectedObserved int      `json:"expected_observed"`
	ExpectedComplete bool     `json:"expected_complete"`
}

type DemoCase struct {
	Name     string    `json:"name"`
	Passed   bool      `json:"passed"`
	Final    State     `json:"final"`
	Outcomes []Outcome `json:"outcomes"`
}

type DemoReport struct {
	Version           string     `json:"version"`
	ForwardTest       bool       `json:"forward_test"`
	ExecutionIncluded bool       `json:"execution_included"`
	Cases             []DemoCase `json:"cases"`
}

// Demo constructs explicit receiver/clock events; it never treats the source
// archive's after-close receipt as historical live receipt.
func Demo(ctx context.Context, fixture marketdata.Dataset, direction r001.Direction, variant r001.Variant) ([]DemoInput, DemoReport, error) {
	r := DemoReport{Version: Version, Cases: []DemoCase{}}
	if fixture.Origin != "synthetic" || fixture.Symbol != "0000@1" || fixture.Environment != "test" || len(fixture.Days) != 1 || len(fixture.Days[0].Bars) != 111 {
		return nil, r, errors.New("確定足入力の模擬試験には組込の合成足が必要です")
	}
	if err := marketdata.Validate(fixture); err != nil {
		return nil, r, err
	}
	config := Config{Date: fixture.Days[0].Date, Symbol: fixture.Symbol, Origin: fixture.Origin, Direction: direction, Variant: variant}
	actions := []Action{}
	for _, bar := range fixture.Days[0].Bars {
		at := bar.Start.Add(time.Minute + SyntheticCloseLag)
		actions = append(actions, Action{At: at, Kind: "offer", Observation: &Observation{ReceivedAt: at, Bar: bar}})
	}
	first := actions[0]
	duplicate := first
	duplicate.At = first.At.Add(time.Second)
	duplicates := append([]Action{first, duplicate}, actions[1:]...)
	revision := *first.Observation
	revision.Bar.Close += marketdata.PriceScale
	revision.Bar.High += marketdata.PriceScale
	early := *first.Observation
	early.ReceivedAt = first.At.Add(-time.Second)
	future := *first.Observation
	future.ReceivedAt = first.At.Add(time.Second)
	gap := *actions[1].Observation
	gap.ReceivedAt = first.At
	observed := config
	observed.Origin, observed.Symbol = "observed", "7203@1"
	inputs := []DemoInput{
		{"ordered_delivery", config, actions, "", 111, true},
		{"identical_duplicate", config, duplicates, "", 111, true},
		{"revised_emitted_minute", config, []Action{first, {At: duplicate.At, Kind: "offer", Observation: &revision}}, "emitted_minute_revised", 1, false},
		{"premature_closed_minute", config, []Action{{At: early.ReceivedAt, Kind: "offer", Observation: &early}}, "minute_not_ready", 0, false},
		{"silence_timeout", config, []Action{{At: first.At.Add(SyntheticArrivalDeadline - SyntheticCloseLag + time.Millisecond), Kind: "tick"}}, "minute_arrival_timeout", 0, false},
		{"future_receiver_time", config, []Action{{At: first.At, Kind: "offer", Observation: &future}}, "invalid_received_time", 0, false},
		{"clock_reversed", config, []Action{first, {At: first.At.Add(-time.Second), Kind: "tick"}}, "observation_clock_reversed_or_invalid", 1, false},
		{"minute_gap", config, []Action{{At: first.At, Kind: "offer", Observation: &gap}}, "minute_gap_or_out_of_order", 0, false},
		{"observed_source_is_not_certified", observed, []Action{first}, "observed_source_validation_pending", 0, false},
	}
	for _, fault := range []string{"transport_failure", "authentication_lost", "rate_limited", "storage_failure", "source_disconnected"} {
		inputs = append(inputs, DemoInput{fault, config, []Action{first, {At: duplicate.At, Kind: "fault", Fault: fault}}, fault, 1, false})
	}
	for _, input := range inputs {
		if ctx.Err() != nil {
			return nil, r, errors.New("確定足入力の模擬試験を中断しました")
		}
		evaluator, err := r001.NewScanner(input.Config.Date, input.Config.Direction, input.Config.Variant)
		if err != nil {
			return nil, r, err
		}
		feed, err := New(input.Config, r001.VersionFor(input.Config.Direction, input.Config.Variant), evaluator)
		if err != nil {
			return nil, r, err
		}
		c := DemoCase{Name: input.Name, Outcomes: []Outcome{}}
		for _, action := range input.Actions {
			var outcome Outcome
			switch action.Kind {
			case "offer":
				outcome = feed.Offer(action.At, *action.Observation)
			case "tick":
				outcome = feed.Tick(action.At)
			case "fault":
				outcome = feed.Fault(action.At, action.Fault)
			}
			c.Outcomes = append(c.Outcomes, outcome)
		}
		c.Final = feed.Snapshot()
		c.Passed = c.Final.StopReason == input.ExpectedStop && c.Final.Halted == (input.ExpectedStop != "") && c.Final.Observed == input.ExpectedObserved && c.Final.Complete == input.ExpectedComplete
		if input.Name == "identical_duplicate" {
			c.Passed = c.Passed && c.Outcomes[1].Duplicate
		}
		for _, signal := range c.Final.Signals {
			c.Passed = c.Passed && signal.ObservedAt.After(signal.Decision.At) && !signal.ObservedAt.Before(signal.ReceivedAt)
		}
		r.Cases = append(r.Cases, c)
	}
	return inputs, r, nil
}
