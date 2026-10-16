// Package replaysignals prepares paper input by replaying completed minutes.
// It has no orders, simulated fills, account, broker, credential, or network.
package replaysignals

import (
	"context"
	"errors"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

const Version = "signal-replay-v0.1"

type Report struct {
	Version           string `json:"version"`
	Rule              string `json:"strategy_version"`
	Origin            string `json:"origin"`
	SourceSHA256      string `json:"source_sha256"`
	ForwardTest       bool   `json:"forward_test"`
	ExecutionIncluded bool   `json:"execution_included"`
	StrategyAdopted   bool   `json:"strategy_adopted"`
	Days              []Day  `json:"days"`
}
type Day struct {
	Date     string   `json:"date_jst"`
	Status   string   `json:"status"`
	Observed int      `json:"observed_closed_minutes"`
	Reasons  []string `json:"reasons"`
	Events   []Event  `json:"events"`
}
type Event struct {
	ObservedAt time.Time     `json:"observed_at"`
	Decision   r001.Decision `json:"decision"`
}

func Run(ctx context.Context, input marketdata.Dataset, direction r001.Direction, variant r001.Variant) (Report, error) {
	r := Report{Version: Version, Rule: r001.VersionFor(direction, variant), Origin: input.Origin, SourceSHA256: input.Source.SHA256, Days: []Day{}}
	if err := marketdata.Validate(input); err != nil {
		return r, err
	}
	if !r001.Valid(direction) || !r001.ValidVariant(variant) {
		return r, errors.New("再生の方向・版を確認してください")
	}
	for _, day := range input.Days {
		if ctx.Err() != nil {
			return r, errors.New("シグナル再生を中断しました")
		}
		d := Day{Date: day.Date, Status: "completed", Reasons: []string{}, Events: []Event{}}
		if input.Origin != "synthetic" || len(day.Issues) > 0 {
			d.Status = "unevaluable"
			d.Reasons = append(d.Reasons, day.Issues...)
			if input.Origin != "synthetic" {
				d.Reasons = append(d.Reasons, "observed_replay_validation_pending")
			}
			r.Days = append(r.Days, d)
			continue
		}
		bars := map[time.Time]marketdata.Bar{}
		for _, b := range day.Bars {
			bars[b.Start.UTC()] = b
		}
		scanner, _ := r001.NewScanner(day.Date, direction, variant)
		for m := 0; m < 111; m++ {
			if ctx.Err() != nil {
				return r, errors.New("シグナル再生を中断しました")
			}
			at := marketdata.Clock(day.Date, 9, 0).Add(time.Duration(m) * time.Minute)
			b, ok := bars[at.UTC()]
			if !ok {
				d.Status = "unevaluable"
				d.Reasons = append(d.Reasons, "required_minute_missing")
				break
			}
			observed := at.Add(time.Minute)
			if observed.After(input.Source.ReceivedAt) {
				d.Status = "unevaluable"
				d.Reasons = append(d.Reasons, "unfinished_minute")
				break
			}
			decision, err := scanner.Observe(observed, b)
			if err != nil {
				d.Status = "unevaluable"
				d.Reasons = append(d.Reasons, "minute_order_or_quality_invalid")
				break
			}
			d.Observed++
			if decision != nil {
				d.Events = append(d.Events, Event{ObservedAt: observed, Decision: *decision})
				if !decision.Accepted && decision.Reason != "volume_below_1_5x_previous_5" {
					d.Status = "unevaluable"
					d.Reasons = append(d.Reasons, decision.Reason)
				}
			}
		}
		r.Days = append(r.Days, d)
	}
	return r, nil
}
