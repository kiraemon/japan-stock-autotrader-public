package papertrade

import (
	"errors"
	"strconv"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

const SessionVersion = "synthetic-timed-quote-session-v0.1"

// MinuteGate is owned by the receiving use case. cmd adapts streamsignals;
// papertrade never imports another feature or bypasses that gate's checks.
type MinuteGate interface {
	Offer(now, received time.Time, bar marketdata.Bar) MinuteDelivery
	Tick(now time.Time) MinuteDelivery
}

type MinuteDelivery struct {
	Accepted, Duplicate, Complete bool
	Reason                        string
	Decision                      *r001.Decision
}

type SessionConfig struct {
	Date        string         `json:"date_jst"`
	Symbol      string         `json:"symbol"`
	Environment string         `json:"environment"`
	Origin      string         `json:"origin"`
	Direction   r001.Direction `json:"direction"`
	Variant     r001.Variant   `json:"variant"`
}

type SessionInput struct {
	Kind       string            `json:"kind"` // minute, quote, tick, fault
	ReceivedAt time.Time         `json:"received_at,omitempty"`
	Bar        *marketdata.Bar   `json:"bar,omitempty"`
	Quote      *marketdata.Quote `json:"quote,omitempty"`
	Fault      string            `json:"fault,omitempty"`
}

type SessionSignal struct {
	ObservedAt time.Time     `json:"observed_at"`
	ReceivedAt time.Time     `json:"received_at"`
	Decision   r001.Decision `json:"decision"`
}

type SessionState struct {
	Version       string         `json:"version"`
	Rule          string         `json:"rule"`
	Config        SessionConfig  `json:"config"`
	Halted        bool           `json:"halted"`
	StopReason    string         `json:"stop_reason,omitempty"`
	Complete      bool           `json:"complete"`
	Observed      int            `json:"observed_closed_minutes"`
	Signal        *SessionSignal `json:"signal,omitempty"`
	EntryIntentAt time.Time      `json:"entry_intent_at,omitempty"`
	FirstFillAt   time.Time      `json:"first_fill_at,omitempty"`
	ExitIntentAt  time.Time      `json:"exit_intent_at,omitempty"`
	ExitReason    string         `json:"exit_reason,omitempty"`
	Final         Snapshot       `json:"final"`
}

type SessionOutcome struct {
	Accepted  bool       `json:"accepted"`
	Duplicate bool       `json:"duplicate"`
	Reason    string     `json:"reason,omitempty"`
	Fill      *QuoteFill `json:"fill,omitempty"`
}

type QuoteSession struct {
	gate      MinuteGate
	ledger    *Simulator
	execution *QuoteExecution
	state     SessionState
	lastClock time.Time
	sequence  int
}

func NewQuoteSession(config SessionConfig, gate MinuteGate) (*QuoteSession, error) {
	if _, err := r001.NewScanner(config.Date, config.Direction, config.Variant); err != nil || gate == nil || config.Origin != "synthetic" || config.Symbol != "0000@1" || config.Environment != "test" {
		return nil, errors.New("時刻順セッションは仮銘柄の合成条件と確定足検査だけを受け付けます")
	}
	ledger, _ := New(config.Direction)
	return &QuoteSession{gate: gate, ledger: ledger, execution: NewQuoteExecution(ledger), state: SessionState{Version: SessionVersion, Rule: r001.VersionFor(config.Direction, config.Variant), Config: config}}, nil
}

func (s *QuoteSession) emit(now time.Time, kind, order string, quantity int64) bool {
	s.sequence++
	result := s.ledger.Apply(Event{ID: "session-" + strconv.Itoa(s.sequence), At: now, Kind: kind, OrderID: order, Quantity: quantity})
	return result.Accepted
}

// Stop retains all quantities and working orders. No cancellation, flattening,
// automatic restart or broker action is inferred from stopping the loop.
func (s *QuoteSession) Stop(now time.Time, reason string) SessionOutcome {
	if !s.state.Halted {
		if now.IsZero() || now.Before(s.lastClock) || marketdata.Date(now) != s.state.Config.Date {
			now = s.lastClock
		}
		if now.IsZero() {
			now = marketdata.Clock(s.state.Config.Date, 9, 0)
		}
		s.state.Halted, s.state.StopReason = true, reason
		if !s.emit(now, "stop", "", 0) {
			s.state.StopReason = "ledger_stop_rejected"
		}
	}
	return SessionOutcome{Reason: s.state.StopReason}
}

func (s *QuoteSession) order(id string) Order {
	for _, order := range s.ledger.Snapshot().Orders {
		if order.ID == id {
			return order
		}
	}
	return Order{}
}

func working(order Order) bool { return order.State == "working" || order.State == "partially_filled" }

func (s *QuoteSession) cancelEntry(now time.Time) bool {
	if !working(s.order("entry")) {
		return true
	}
	// Immediate cancel acknowledgement is a synthetic assumption, never a
	// statement about broker cancellation or a real order/acknowledgement race.
	return s.emit(now, "cancel_requested", "entry", 0) && s.emit(now, "canceled", "entry", 0)
}

func (s *QuoteSession) exit(now time.Time, reason string) bool {
	if !s.state.ExitIntentAt.IsZero() || s.ledger.Snapshot().OpenQuantity == 0 {
		return true
	}
	if !s.cancelEntry(now) {
		return false
	}
	s.state.ExitIntentAt, s.state.ExitReason = now, reason
	quantity := s.ledger.Snapshot().OpenQuantity
	return s.emit(now, "close_intent", "exit", quantity) && s.emit(now, "accepted", "exit", 0)
}

func (s *QuoteSession) advance(now time.Time) SessionOutcome {
	if s.state.Halted {
		return SessionOutcome{Reason: s.state.StopReason}
	}
	if s.state.Complete {
		return SessionOutcome{Reason: "session_complete"}
	}
	if now.IsZero() || marketdata.Date(now) != s.state.Config.Date || now.Before(marketdata.Clock(s.state.Config.Date, 9, 0)) || !s.lastClock.IsZero() && now.Before(s.lastClock) {
		return s.Stop(now, "observation_clock_reversed_or_invalid")
	}
	s.lastClock = now
	if delivery := s.gate.Tick(now); !delivery.Accepted {
		return s.Stop(now, delivery.Reason)
	}
	if !s.state.EntryIntentAt.IsZero() && !now.Before(s.state.EntryIntentAt.Add(SyntheticEntryTTLSeconds*time.Second)) && !s.cancelEntry(now) {
		return s.Stop(now, "ledger_cancel_rejected")
	}
	if !s.state.FirstFillAt.IsZero() && !now.Before(s.state.FirstFillAt.Add(r001.MaximumHold)) && !s.exit(now, "maximum_hold") {
		return s.Stop(now, "ledger_exit_rejected")
	}
	if working(s.order("exit")) && !now.Before(s.state.ExitIntentAt.Add(SyntheticEntryTTLSeconds*time.Second)) {
		return s.Stop(now, "exit_fill_timeout")
	}
	return SessionOutcome{Accepted: true}
}

// Step processes an event at observation time, never at a queued ticker's old
// timestamp or a bar's close. Callers must serialise Step and Stop.
func (s *QuoteSession) Step(now time.Time, input SessionInput) SessionOutcome {
	if out := s.advance(now); !out.Accepted {
		return out
	}
	switch input.Kind {
	case "tick":
		return SessionOutcome{Accepted: true}
	case "fault":
		if input.Fault == "execution_unknown" {
			id := "entry"
			if working(s.order("exit")) {
				id = "exit"
			}
			if !working(s.order(id)) || !s.emit(now, "transport_unknown", id, 0) {
				return s.Stop(now, "execution_unknown_without_working_order")
			}
			return s.Stop(now, "execution_unknown")
		}
		switch input.Fault {
		case "transport_failure", "authentication_lost", "rate_limited", "storage_failure", "source_disconnected":
			return s.Stop(now, input.Fault)
		default:
			return s.Stop(now, "unknown_source_failure")
		}
	case "minute":
		if input.Bar == nil {
			return s.Stop(now, "invalid_session_input")
		}
		d := s.gate.Offer(now, input.ReceivedAt, *input.Bar)
		if !d.Accepted {
			return s.Stop(now, d.Reason)
		}
		if d.Duplicate {
			return SessionOutcome{Accepted: true, Duplicate: true}
		}
		s.state.Observed++
		if d.Decision != nil {
			if s.state.Signal != nil || !d.Decision.At.Equal(input.Bar.Start.Add(time.Minute)) || d.Decision.At.After(now) || d.Decision.Boundary <= 0 || d.Decision.Boundary > marketdata.MaxPrice {
				return s.Stop(now, "strategy_decision_invalid")
			}
			s.state.Signal = &SessionSignal{ObservedAt: now, ReceivedAt: input.ReceivedAt, Decision: copyDecision(*d.Decision)}
			if d.Decision.Accepted {
				s.state.EntryIntentAt = now
				if !s.emit(now, "open_intent", "entry", SyntheticEntryQuantity) || !s.emit(now, "accepted", "entry", 0) {
					return s.Stop(now, "ledger_entry_rejected")
				}
			} else if d.Decision.Reason != "volume_below_1_5x_previous_5" {
				return s.Stop(now, d.Decision.Reason)
			}
		}
		if s.state.Signal != nil && s.state.Signal.Decision.Accepted && r001.Reversed(s.state.Config.Direction, input.Bar.Close, s.state.Signal.Decision.Boundary) && !s.exit(now, "range_reversal") {
			return s.Stop(now, "ledger_exit_rejected")
		}
		if d.Complete {
			if !s.ledger.Snapshot().CleanFinish {
				return s.Stop(now, "window_ended_with_exposure")
			}
			s.state.Complete = true
		}
		return SessionOutcome{Accepted: true}
	case "quote":
		if input.Quote == nil {
			return s.Stop(now, "invalid_session_input")
		}
		if input.Quote.Origin != "synthetic" || input.Quote.Symbol != "0000@1" || input.Quote.Environment != "test" {
			return s.Stop(now, "observed_quote_execution_pending")
		}
		order := "entry"
		if !s.state.ExitIntentAt.IsZero() {
			order = "exit"
		}
		fill := s.execution.TryFill(order, now, *input.Quote)
		if fill.Filled && order == "entry" && s.state.FirstFillAt.IsZero() {
			s.state.FirstFillAt = now
		}
		return SessionOutcome{Accepted: true, Fill: &fill}
	default:
		return s.Stop(now, "invalid_session_input")
	}
}

func copyDecision(d r001.Decision) r001.Decision {
	if d.Volume != nil {
		v := *d.Volume
		if v.SignalVolume != nil {
			n := *v.SignalVolume
			v.SignalVolume = &n
		}
		d.Volume = &v
	}
	return d
}

func (s *QuoteSession) Snapshot() SessionState {
	state := s.state
	state.Final = s.ledger.Snapshot()
	if state.Signal != nil {
		signal := *state.Signal
		signal.Decision = copyDecision(signal.Decision)
		state.Signal = &signal
	}
	return state
}
