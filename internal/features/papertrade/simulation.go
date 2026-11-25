// Package papertrade implements offline, synthetic quantity-ledger experiments.
// It has no broker, transport, credentials, or capability to submit real orders.
package papertrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy"
)

const Version = "synthetic-quantity-ledger-v0.1"
const maxEvents = 10000

// This bounds synthetic input, and is not an account risk/position setting.
const maxSyntheticQuantity int64 = 1000000

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Event struct {
	ID               string    `json:"id"`
	At               time.Time `json:"at"`
	Kind             string    `json:"kind"`
	OrderID          string    `json:"virtual_order_id,omitempty"`
	Quantity         int64     `json:"quantity,omitempty"`
	CumulativeFilled int64     `json:"cumulative_filled,omitempty"`
	AveragePrice     int64     `json:"average_price_scaled,omitempty"`
	ResolvedState    string    `json:"resolved_state,omitempty"`
	ObservedPosition int64     `json:"observed_position_quantity,omitempty"`
}
type Order struct {
	ID              string `json:"id"`
	Purpose         string `json:"purpose"`
	Quantity        int64  `json:"quantity"`
	Filled          int64  `json:"filled"`
	AveragePrice    int64  `json:"average_price_scaled"`
	State           string `json:"state"`
	CancelRequested bool   `json:"cancel_requested"`
}
type Outcome struct {
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	Reason    string `json:"reason,omitempty"`
}
type Record struct {
	Event   Event   `json:"event"`
	Outcome Outcome `json:"outcome"`
}
type Snapshot struct {
	Version           string             `json:"version"`
	Symbol            string             `json:"symbol"`
	Direction         strategy.Direction `json:"direction"`
	OpenQuantity      int64              `json:"open_quantity"`
	SignedQuantity    int64              `json:"signed_quantity"`
	PositionKnown     bool               `json:"position_known"`
	Halted            bool               `json:"new_entries_halted"`
	ReconcileRequired bool               `json:"reconcile_required"`
	ActiveOrders      int                `json:"active_orders"`
	RemainingQuantity int64              `json:"remaining_quantity"`
	CleanFinish       bool               `json:"clean_finish"`
	Orders            []Order            `json:"orders"`
	Records           []Record           `json:"records"`
}
type Simulator struct {
	direction         strategy.Direction
	position          int64
	halted, reconcile bool
	orders            map[string]Order
	seen              map[string]seenEvent
	lastAt            time.Time
	journal           []Record
}
type seenEvent struct {
	fingerprint string
	outcome     Outcome
}

func New(direction strategy.Direction) (*Simulator, error) {
	if !strategy.ValidDirection(direction) {
		return nil, errors.New("合成台帳の方向を確認してください")
	}
	return &Simulator{direction: direction, orders: map[string]Order{}, seen: map[string]seenEvent{}, journal: []Record{}}, nil
}

func (s *Simulator) Apply(e Event) Outcome {
	if len(s.journal) >= maxEvents || !idPattern.MatchString(e.ID) || e.At.IsZero() || e.At.Year() < 1 || e.At.Year() > 9999 {
		return Outcome{Reason: "invalid_event_or_capacity"}
	}
	raw, _ := json.Marshal(e)
	fingerprint := digest(raw)
	if previous, exists := s.seen[e.ID]; exists {
		out := previous.outcome
		out.Duplicate = true
		if previous.fingerprint != fingerprint {
			s.halted = true
			s.reconcile = true
			out = Outcome{Reason: "event_id_conflict"}
		}
		s.journal = append(s.journal, Record{e, out})
		return out
	}
	var out Outcome
	if !s.lastAt.IsZero() && e.At.Before(s.lastAt) {
		s.halted = true
		s.reconcile = true
		out = Outcome{Reason: "event_time_reversed"}
	} else {
		out = s.reduce(e)
		s.lastAt = e.At
	}
	s.seen[e.ID] = seenEvent{fingerprint, out}
	s.journal = append(s.journal, Record{e, out})
	return out
}

func (s *Simulator) reduce(e Event) Outcome {
	ok := Outcome{Accepted: true}
	if e.Kind == "stop" {
		s.halted = true
		return ok
	}
	if !idPattern.MatchString(e.OrderID) {
		return Outcome{Reason: "invalid_virtual_order_id"}
	}
	o, exists := s.orders[e.OrderID]
	if e.Kind == "open_intent" || e.Kind == "close_intent" {
		purpose := "open"
		if e.Kind == "close_intent" {
			purpose = "close"
		}
		if exists {
			if o.Purpose == purpose && o.Quantity == e.Quantity {
				return Outcome{Accepted: true, Duplicate: true}
			}
			s.halted = true
			s.reconcile = true
			return Outcome{Reason: "intent_id_conflict"}
		}
		if e.Quantity <= 0 || e.Quantity > maxSyntheticQuantity {
			return Outcome{Reason: "invalid_synthetic_quantity"}
		}
		if s.reconcile {
			return Outcome{Reason: "reconciliation_required"}
		}
		if purpose == "open" {
			if s.halted || s.position != 0 || s.activeOrders() != 0 {
				return Outcome{Reason: "new_entry_blocked"}
			}
		} else if e.Quantity > s.position-s.closeReserved() {
			return Outcome{Reason: "close_exceeds_unreserved_position"}
		}
		s.orders[e.OrderID] = Order{ID: e.OrderID, Purpose: purpose, Quantity: e.Quantity, State: "submitted"}
		return ok
	}
	if !exists {
		s.halted = true
		s.reconcile = true
		return Outcome{Reason: "unknown_virtual_order"}
	}
	conflict := func(reason string) Outcome { s.halted = true; s.reconcile = true; return Outcome{Reason: reason} }
	switch e.Kind {
	case "accepted":
		if o.State == "submitted" {
			o.State = "working"
		}
	case "fill":
		if e.CumulativeFilled < o.Filled || e.CumulativeFilled > o.Quantity || e.CumulativeFilled <= 0 || e.AveragePrice <= 0 || e.AveragePrice > marketdata.MaxPrice {
			return conflict("invalid_cumulative_fill")
		}
		if e.CumulativeFilled == o.Filled {
			if e.AveragePrice != o.AveragePrice {
				return conflict("same_quantity_price_conflict")
			}
			return Outcome{Accepted: true, Duplicate: true}
		}
		delta := e.CumulativeFilled - o.Filled
		if o.Purpose == "close" && delta > s.position {
			return conflict("close_fill_exceeds_position")
		}
		if o.Purpose == "open" {
			s.position += delta
		} else {
			s.position -= delta
		}
		o.Filled = e.CumulativeFilled
		o.AveragePrice = e.AveragePrice
		switch o.State {
		case "unknown": // Keep unknown until an explicit reconciliation.
		case "canceled", "rejected":
			o.State = "unknown"
			s.halted = true
			s.reconcile = true
		default:
			if o.Filled == o.Quantity {
				o.State = "filled"
			} else if o.State != "cancel_pending" {
				o.State = "partially_filled"
			}
		}
	case "cancel_requested":
		if o.State == "unknown" {
			return conflict("unknown_order_needs_reconciliation")
		}
		if terminal(o.State) {
			return Outcome{Reason: "terminal_order"}
		}
		o.CancelRequested = true
		o.State = "cancel_pending"
	case "canceled":
		if !o.CancelRequested {
			return conflict("unexpected_cancel_confirmation")
		}
		if o.State != "unknown" && o.State != "filled" {
			o.State = "canceled"
		}
	case "rejected":
		if o.Filled != 0 {
			return conflict("reject_after_fill")
		}
		if o.State == "unknown" {
			return conflict("unknown_order_needs_reconciliation")
		}
		o.State = "rejected"
	case "transport_unknown":
		o.State = "unknown"
		s.halted = true
		s.reconcile = true
	case "reconciled":
		if e.CumulativeFilled < o.Filled || e.CumulativeFilled > o.Quantity || !resolved(e.ResolvedState, e.CumulativeFilled, o.Quantity) || (e.CumulativeFilled > 0 && (e.AveragePrice <= 0 || e.AveragePrice > marketdata.MaxPrice)) || (e.CumulativeFilled == o.Filled && e.AveragePrice != o.AveragePrice) {
			return conflict("invalid_reconciliation")
		}
		delta := e.CumulativeFilled - o.Filled
		position := s.position + delta
		if o.Purpose == "close" {
			position = s.position - delta
		}
		if position < 0 || position != e.ObservedPosition {
			return conflict("reconciled_position_mismatch")
		}
		reserved := s.closeReserved()
		if o.Purpose == "close" {
			if !terminal(o.State) {
				reserved -= o.Quantity - o.Filled
			}
			if !terminal(e.ResolvedState) {
				reserved += o.Quantity - e.CumulativeFilled
			}
		}
		if reserved > position {
			return conflict("outstanding_close_reservation_conflict")
		}
		s.position = position
		o.Filled = e.CumulativeFilled
		o.AveragePrice = e.AveragePrice
		o.State = e.ResolvedState
		s.orders[e.OrderID] = o
		for _, order := range s.orders {
			if order.State == "unknown" {
				return ok
			}
		}
		s.reconcile = false // New entries remain halted after recovery.
		return ok
	default:
		return conflict("unknown_event_kind")
	}
	s.orders[e.OrderID] = o
	if s.closeReserved() > s.position {
		s.halted = true
		s.reconcile = true
	}
	return ok
}

func terminal(state string) bool {
	return state == "filled" || state == "canceled" || state == "rejected"
}
func resolved(state string, filled, quantity int64) bool {
	switch state {
	case "filled":
		return filled == quantity
	case "rejected":
		return filled == 0
	case "canceled":
		return filled < quantity
	case "working":
		return filled == 0
	case "partially_filled":
		return filled > 0 && filled < quantity
	}
	return false
}
func (s *Simulator) activeOrders() int {
	n := 0
	for _, o := range s.orders {
		if !terminal(o.State) {
			n++
		}
	}
	return n
}
func (s *Simulator) closeReserved() int64 {
	var n int64
	for _, o := range s.orders {
		if o.Purpose == "close" && !terminal(o.State) {
			n += o.Quantity - o.Filled
		}
	}
	return n
}

func (s *Simulator) Snapshot() Snapshot {
	r := Snapshot{Version: Version, Symbol: "0000@1", Direction: s.direction, OpenQuantity: s.position, SignedQuantity: s.position, PositionKnown: !s.reconcile, Halted: s.halted, ReconcileRequired: s.reconcile, ActiveOrders: s.activeOrders(), Orders: []Order{}, Records: append([]Record{}, s.journal...)}
	if s.direction == strategy.Short {
		r.SignedQuantity = -r.SignedQuantity
	}
	for _, o := range s.orders {
		r.Orders = append(r.Orders, o)
		if !terminal(o.State) {
			r.RemainingQuantity += o.Quantity - o.Filled
		}
	}
	sort.Slice(r.Orders, func(i, j int) bool { return r.Orders[i].ID < r.Orders[j].ID })
	r.CleanFinish = r.PositionKnown && r.OpenQuantity == 0 && r.ActiveOrders == 0
	return r
}

type checkpoint struct {
	Version     string             `json:"version"`
	Direction   strategy.Direction `json:"direction"`
	Events      []Event            `json:"synthetic_events"`
	StateSHA256 string             `json:"state_sha256"`
}

func (s *Simulator) Checkpoint() ([]byte, error) {
	snapshot, err := json.Marshal(s.Snapshot())
	if err != nil {
		return nil, errors.New("合成台帳の状態を保存できません")
	}
	c := checkpoint{Version: Version, Direction: s.direction, Events: []Event{}, StateSHA256: digest(snapshot)}
	for _, r := range s.journal {
		c.Events = append(c.Events, r.Event)
	}
	return json.Marshal(c)
}
func Restore(raw []byte) (*Simulator, error) {
	if len(raw) > 8<<20 {
		return nil, errors.New("合成チェックポイントが大きすぎます")
	}
	var c checkpoint
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Version != Version || len(c.Events) > maxEvents {
		return nil, errors.New("合成チェックポイントの形式を確認してください")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("合成チェックポイントに余分なデータがあります")
	}
	s, err := New(c.Direction)
	if err != nil {
		return nil, err
	}
	for _, e := range c.Events {
		s.Apply(e)
	}
	snapshot, err := json.Marshal(s.Snapshot())
	if err != nil || digest(snapshot) != c.StateSHA256 {
		return nil, errors.New("合成チェックポイントのイベントと状態hashが一致しません")
	}
	return s, nil
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
