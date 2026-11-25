// Package streamsignals checks minute delivery using an injected observation
// clock. It does not assemble ticks, certify a source, simulate fills, or submit orders.
package streamsignals

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

const Version = "closed-minute-delivery-v0.1"

// Fixed synthetic experiment parameters, not broker latency guarantees.
const SyntheticCloseLag = 2 * time.Second
const SyntheticArrivalDeadline = 10 * time.Second

type Config struct {
	Date      string         `json:"date_jst"`
	Symbol    string         `json:"symbol"`
	Origin    string         `json:"origin"`
	Direction r001.Direction `json:"direction"`
	Variant   r001.Variant   `json:"variant"`
}

type Observation struct {
	ReceivedAt time.Time      `json:"received_at"`
	Bar        marketdata.Bar `json:"bar"`
}

type Outcome struct {
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	Reason    string `json:"reason,omitempty"`
}

type Signal struct {
	ObservedAt time.Time     `json:"observed_at"`
	ReceivedAt time.Time     `json:"received_at"`
	Decision   r001.Decision `json:"decision"`
}

type State struct {
	Version    string    `json:"version"`
	Rule       string    `json:"rule"`
	Halted     bool      `json:"halted"`
	StopReason string    `json:"stop_reason,omitempty"`
	Observed   int       `json:"observed_closed_minutes"`
	Complete   bool      `json:"complete"`
	NextStart  time.Time `json:"next_minute_start"`
	Signals    []Signal  `json:"signals"`
}

type Feed struct {
	config           Config
	scanner          *r001.Scanner
	start, lastClock time.Time
	seen             map[time.Time][32]byte
	state            State
}

func New(config Config) (*Feed, error) {
	scanner, err := r001.NewScanner(config.Date, config.Direction, config.Variant)
	if err != nil || (config.Origin != "synthetic" && config.Origin != "observed") || config.Symbol != "0000@1" && config.Origin == "synthetic" {
		return nil, errors.New("確定足入力の設定を確認してください")
	}
	start := marketdata.Clock(config.Date, 9, 0)
	return &Feed{config: config, scanner: scanner, start: start, seen: map[time.Time][32]byte{}, state: State{Version: Version, Rule: r001.VersionFor(config.Direction, config.Variant), NextStart: start, Signals: []Signal{}}}, nil
}

func (f *Feed) halt(reason string) Outcome {
	if !f.state.Halted {
		f.state.Halted, f.state.StopReason = true, reason
	}
	return Outcome{Reason: f.state.StopReason}
}

func (f *Feed) clock(now time.Time) Outcome {
	if f.state.Halted {
		return Outcome{Reason: f.state.StopReason}
	}
	if now.IsZero() || now.Year() < 1 || now.Year() > 9999 || now.Before(f.start) || !f.lastClock.IsZero() && now.Before(f.lastClock) {
		return f.halt("observation_clock_reversed_or_invalid")
	}
	f.lastClock = now
	if !f.state.Complete && now.After(f.state.NextStart.Add(time.Minute+SyntheticArrivalDeadline)) {
		return f.halt("minute_arrival_timeout")
	}
	return Outcome{Accepted: true}
}

// Tick checks silence even when no input arrives. Repeated old data does not
// refresh this deadline. A future runner must call it from its own timer.
func (f *Feed) Tick(now time.Time) Outcome { return f.clock(now) }

// Offer receives one completed minute. The caller supplies observation time;
// received_at is independent of market time and must not be in the future.
func (f *Feed) Offer(now time.Time, observation Observation) Outcome {
	if out := f.clock(now); !out.Accepted {
		return out
	}
	if f.config.Origin != "synthetic" {
		return f.halt("observed_source_validation_pending")
	}
	b, received := observation.Bar, observation.ReceivedAt
	if received.IsZero() || received.After(now) {
		return f.halt("invalid_received_time")
	}
	if marketdata.Date(b.Start) != f.config.Date || !b.Start.Equal(b.Start.Truncate(time.Minute)) || !marketdata.ValidPrices(b) || len(b.Issues) != 0 || b.Volume != nil && *b.Volume < 0 {
		return f.halt("minute_identity_or_quality_invalid")
	}
	// Compare economic bar contents, not receiver metadata or source row offsets.
	raw, _ := json.Marshal(struct {
		Start                  time.Time
		Open, High, Low, Close int64
		Volume                 *int64
	}{b.Start.UTC(), b.Open, b.High, b.Low, b.Close, b.Volume})
	fingerprint := sha256.Sum256(raw)
	key := b.Start.UTC()
	if previous, exists := f.seen[key]; exists {
		if previous != fingerprint {
			return f.halt("emitted_minute_revised")
		}
		return Outcome{Accepted: true, Duplicate: true}
	}
	if f.state.Complete {
		return f.halt("outside_research_window")
	}
	if !b.Start.Equal(f.state.NextStart) {
		return f.halt("minute_gap_or_out_of_order")
	}
	end := b.Start.Add(time.Minute)
	if received.Before(end.Add(SyntheticCloseLag)) || now.Before(end.Add(SyntheticCloseLag)) {
		return f.halt("minute_not_ready")
	}
	if received.After(end.Add(SyntheticArrivalDeadline)) || now.After(end.Add(SyntheticArrivalDeadline)) {
		return f.halt("minute_arrival_timeout")
	}
	decision, err := f.scanner.Observe(now, b)
	if err != nil {
		return f.halt("scanner_input_rejected")
	}
	f.seen[key] = fingerprint
	f.state.Observed++
	f.state.NextStart = f.start.Add(time.Duration(f.state.Observed) * time.Minute)
	f.state.Complete = f.state.Observed == 111
	if decision != nil {
		f.state.Signals = append(f.state.Signals, Signal{ObservedAt: now, ReceivedAt: received, Decision: *decision})
		if !decision.Accepted && decision.Reason != "volume_below_1_5x_previous_5" {
			return f.halt(decision.Reason)
		}
	}
	return Outcome{Accepted: true}
}

// Fault records a terminal upstream failure. It never restarts or infers a
// position from the absence of data; no quantity ledger is owned here.
func (f *Feed) Fault(now time.Time, reason string) Outcome {
	if out := f.clock(now); !out.Accepted {
		return out
	}
	switch reason {
	case "transport_failure", "authentication_lost", "rate_limited", "storage_failure", "source_disconnected":
		return f.halt(reason)
	default:
		return f.halt("unknown_source_failure")
	}
}

func (f *Feed) Snapshot() State {
	s := f.state
	s.Signals = append([]Signal{}, f.state.Signals...)
	for i := range s.Signals {
		if volume := s.Signals[i].Decision.Volume; volume != nil {
			copy := *volume
			if volume.SignalVolume != nil {
				quantity := *volume.SignalVolume
				copy.SignalVolume = &quantity
			}
			s.Signals[i].Decision.Volume = &copy
		}
	}
	return s
}
