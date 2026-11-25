package papertrade

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

type DemoReport struct {
	Version                  string     `json:"version"`
	Mode                     string     `json:"mode"`
	ForwardTest              bool       `json:"forward_test"`
	LiveSubmissionCapability bool       `json:"live_submission_capability"`
	Cases                    []DemoCase `json:"cases"`
}
type DemoCase struct {
	Name                string          `json:"name"`
	Passed              bool            `json:"passed"`
	ExpectedCleanFinish bool            `json:"expected_clean_finish"`
	CheckpointVerified  bool            `json:"checkpoint_verified"`
	Final               Snapshot        `json:"final"`
	Checkpoint          json.RawMessage `json:"checkpoint"`
}
type expectation struct {
	quantity      int64
	known, halted bool
	active        int
	clean         bool
}
type scenario struct {
	name         string
	direction    r001.Direction
	events       []Event
	reject       map[string]string
	expected     expectation
	restartAfter int
}

func syntheticEvent(id, kind, order string, second int) Event {
	return Event{ID: id, Kind: kind, OrderID: order, At: marketdata.Clock("2026-10-16", 9, 16).Add(time.Duration(second) * time.Second)}
}
func syntheticIntent(id, kind, order string, second int, quantity int64) Event {
	e := syntheticEvent(id, kind, order, second)
	e.Quantity = quantity
	return e
}
func syntheticFill(id, order string, second int, quantity int64) Event {
	e := syntheticEvent(id, "fill", order, second)
	e.CumulativeFilled = quantity
	e.AveragePrice = 100 * marketdata.PriceScale
	return e
}

func Demo(ctx context.Context) (DemoReport, error) {
	r := DemoReport{Version: Version, Mode: "synthetic_quantity_ledger", Cases: []DemoCase{}}
	reconciled := syntheticEvent("reconcile", "reconciled", "o", 2)
	reconciled.ResolvedState = "canceled"
	partial := []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticEvent("a", "accepted", "o", 1), syntheticFill("f1", "o", 2, 40), syntheticEvent("c", "cancel_requested", "o", 3), syntheticFill("f2", "o", 4, 60), syntheticEvent("done", "canceled", "o", 5), syntheticIntent("x", "close_intent", "x", 6, 60), syntheticFill("xf", "x", 7, 60)}
	cases := []scenario{
		{"long_partial_cancel_then_exit", r001.Long, partial, nil, expectation{0, true, false, 0, true}, 2},
		{"short_partial_cancel_then_exit", r001.Short, partial, nil, expectation{0, true, false, 0, true}, 2},
		{"unknown_is_not_flat", r001.Long, []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticEvent("u", "transport_unknown", "o", 1), syntheticIntent("retry", "open_intent", "second", 2, 100)}, map[string]string{"retry": "reconciliation_required"}, expectation{0, false, true, 1, false}, 1},
		{"explicit_reconciliation_keeps_halt", r001.Long, []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticEvent("u", "transport_unknown", "o", 1), reconciled, syntheticIntent("retry", "open_intent", "second", 3, 100)}, map[string]string{"retry": "new_entry_blocked"}, expectation{0, true, true, 0, true}, 1},
		{"rejected_without_fill", r001.Long, []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticEvent("r", "rejected", "o", 1)}, nil, expectation{0, true, false, 0, true}, -1},
		{"stop_preserves_unfilled_order", r001.Long, []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticEvent("a", "accepted", "o", 1), syntheticEvent("s", "stop", "", 2)}, nil, expectation{0, true, true, 1, false}, -1},
		{"overfill_is_not_flat", r001.Long, []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticFill("bad", "o", 1, 101)}, map[string]string{"bad": "invalid_cumulative_fill"}, expectation{0, false, true, 1, false}, -1},
		{"late_fill_after_cancel", r001.Short, []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticEvent("c", "cancel_requested", "o", 1), syntheticEvent("done", "canceled", "o", 2), syntheticFill("late", "o", 3, 20)}, nil, expectation{20, false, true, 1, false}, 2},
		{"duplicate_fill_after_checkpoint", r001.Long, []Event{syntheticIntent("i", "open_intent", "o", 0, 100), syntheticFill("f", "o", 1, 40), syntheticFill("f", "o", 1, 40), syntheticFill("f2", "o", 2, 100), syntheticIntent("x", "close_intent", "x", 3, 100), syntheticFill("xf", "x", 4, 100)}, nil, expectation{0, true, false, 0, true}, 1},
	}
	for _, spec := range cases {
		if ctx.Err() != nil {
			return r, errors.New("合成台帳の模擬試験を中断しました")
		}
		s, _ := New(spec.direction)
		c := DemoCase{Name: spec.name, Passed: true, ExpectedCleanFinish: spec.expected.clean}
		for i, e := range spec.events {
			out := s.Apply(e)
			if want, exists := spec.reject[e.ID]; exists {
				if out.Accepted || out.Reason != want {
					c.Passed = false
				}
			} else if !out.Accepted {
				c.Passed = false
			}
			if i == spec.restartAfter {
				raw, err := s.Checkpoint()
				if err != nil {
					return r, err
				}
				s, err = Restore(raw)
				if err != nil {
					return r, err
				}
			}
		}
		c.Final = s.Snapshot()
		want := spec.expected
		if c.Final.OpenQuantity != want.quantity || c.Final.PositionKnown != want.known || c.Final.Halted != want.halted || c.Final.ActiveOrders != want.active || c.Final.CleanFinish != want.clean {
			c.Passed = false
		}
		raw, err := s.Checkpoint()
		if err != nil {
			return r, err
		}
		other, err := Restore(raw)
		if err != nil {
			return r, err
		}
		left, _ := json.Marshal(c.Final)
		right, _ := json.Marshal(other.Snapshot())
		c.CheckpointVerified = string(left) == string(right)
		c.Checkpoint = raw
		c.Passed = c.Passed && c.CheckpointVerified
		r.Cases = append(r.Cases, c)
	}
	return r, nil
}
