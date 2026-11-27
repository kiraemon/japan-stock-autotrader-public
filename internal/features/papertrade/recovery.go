package papertrade

import (
	"errors"
	"strconv"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy"
)

// RecoveryRequest is the input contract of the synthetic recovery demo.
// A nil observation is different from an explicitly confirmed zero balance.
type RecoveryRequest struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	OrderID          string `json:"order_id,omitempty"`
	FillEventID      string `json:"fill_event_id,omitempty"`
	CumulativeFilled int64  `json:"cumulative_filled,omitempty"`
	AveragePrice     int64  `json:"average_price_scaled,omitempty"`
	ResolvedState    string `json:"resolved_state,omitempty"`
	ObservedPosition *int64 `json:"observed_position_quantity,omitempty"`
	Quantity         int64  `json:"quantity,omitempty"`
}

type RecoveryResult struct {
	Outcome
	Fill *QuoteFill `json:"fill,omitempty"`
}

func (r RecoveryRequest) Validate() error {
	if !idPattern.MatchString(r.ID) {
		return errors.New("復旧操作には一意のIDを指定してください")
	}
	switch r.Kind {
	case "reconcile", "repeat_fill", "open", "close":
		return nil
	default:
		return errors.New("復旧操作は reconcile / repeat_fill / open / close を指定してください")
	}
}

// StartRecoveryDemo creates the sole supported recovery case, with a real
// quote-model fill of 40 shares and an unknown outcome on the remaining 60.
func StartRecoveryDemo() (*Simulator, error) {
	s, err := New(strategy.Long)
	if err != nil {
		return nil, err
	}
	for _, event := range []Event{
		syntheticIntent("entry-intent", "open_intent", "entry", 0, 100),
		syntheticEvent("entry-accepted", "accepted", "entry", 0),
		syntheticFill("entry-fill", "entry", 1, 100),
		syntheticIntent("exit-intent", "close_intent", "exit", 2, 100),
		syntheticEvent("exit-accepted", "accepted", "exit", 2),
	} {
		if !s.Apply(event).Accepted {
			return nil, errors.New("合成注文を台帳に記録できません")
		}
	}
	quote := recoveryQuote(s.lastAt.Add(time.Second), 40)
	if fill := NewQuoteExecution(s).TryFill("exit", quote.ReceivedAt, quote); !fill.Filled || fill.Delta != 40 {
		return nil, errors.New("最初の仮想決済を記録できません")
	}
	for _, event := range []Event{
		syntheticEvent("exit-unknown", "transport_unknown", "exit", 4),
		syntheticEvent("stop", "stop", "", 4),
	} {
		if !s.Apply(event).Accepted {
			return nil, errors.New("合成停止を台帳に記録できません")
		}
	}
	return s, nil
}

// ApplyRecovery never starts a strategy or sends an order. The caller durably
// records the request and outcome before reporting it to the operator.
func (s *Simulator) ApplyRecovery(request RecoveryRequest) (RecoveryResult, error) {
	if err := request.Validate(); err != nil {
		return RecoveryResult{}, err
	}
	if !s.halted {
		return RecoveryResult{}, errors.New("停止した合成台帳が必要です")
	}
	at := s.lastAt.Add(time.Second)
	switch request.Kind {
	case "repeat_fill":
		for _, record := range s.journal {
			if record.Event.ID == request.FillEventID && record.Event.Kind == "fill" && record.Outcome.Accepted {
				return RecoveryResult{Outcome: s.Apply(record.Event)}, nil
			}
		}
		return RecoveryResult{Outcome: Outcome{Reason: "processed_fill_not_found"}}, nil
	case "reconcile":
		if request.ObservedPosition == nil {
			return RecoveryResult{Outcome: Outcome{Reason: "observed_position_required"}}, nil
		}
		if order := s.orders[request.OrderID]; order.State != "unknown" || order.Purpose != "close" || !terminal(request.ResolvedState) {
			return RecoveryResult{Outcome: Outcome{Reason: "unknown_close_and_terminal_result_required"}}, nil
		}
		e := s.recoveryEvent(at, "reconciled", request.OrderID)
		e.CumulativeFilled = request.CumulativeFilled
		e.AveragePrice = request.AveragePrice
		e.ResolvedState = request.ResolvedState
		e.ObservedPosition = *request.ObservedPosition
		return RecoveryResult{Outcome: s.Apply(e)}, nil
	case "open":
		e := s.recoveryEvent(at, "open_intent", "recovery-open")
		e.Quantity = request.Quantity
		return RecoveryResult{Outcome: s.Apply(e)}, nil
	case "close":
		return s.closeDuringRecovery(at, request.Quantity)
	default:
		return RecoveryResult{}, errors.New("復旧操作を処理できません")
	}
}

func (s *Simulator) closeDuringRecovery(at time.Time, quantity int64) (RecoveryResult, error) {
	if s.reconcile {
		return RecoveryResult{Outcome: Outcome{Reason: "reconciliation_required"}}, nil
	}
	if s.activeOrders() != 0 {
		return RecoveryResult{Outcome: Outcome{Reason: "recovery_has_active_orders"}}, nil
	}
	intent := s.recoveryEvent(at, "close_intent", "")
	intent.OrderID = intent.ID
	intent.Quantity = quantity
	if out := s.Apply(intent); !out.Accepted {
		return RecoveryResult{Outcome: out}, nil
	}
	if !s.Apply(s.recoveryEvent(at, "accepted", intent.OrderID)).Accepted {
		return RecoveryResult{}, errors.New("復旧時の仮想決済を受理できません")
	}
	q := recoveryQuote(at.Add(time.Second), quantity)
	fill := NewQuoteExecution(s).tryFill(intent.OrderID, q.ReceivedAt, q, true)
	if !fill.Filled || fill.Delta != quantity {
		return RecoveryResult{}, errors.New("復旧時の合成気配で指定数量を決済できません")
	}
	return RecoveryResult{Outcome: Outcome{Accepted: true}, Fill: &fill}, nil
}

func (s *Simulator) recoveryEvent(at time.Time, kind, orderID string) Event {
	return Event{ID: "recovery-" + strconv.Itoa(len(s.journal)+1), At: at, Kind: kind, OrderID: orderID}
}

func recoveryQuote(at time.Time, quantity int64) marketdata.Quote {
	return marketdata.Quote{
		Version: marketdata.QuoteVersion, Symbol: "0000@1", Environment: "test", Origin: "synthetic",
		ReceivedAt: at.Add(100 * time.Millisecond),
		Bid:        marketdata.QuoteSide{Price: 99 * marketdata.PriceScale, Quantity: &quantity, Time: at, Sign: "0101"},
		Ask:        marketdata.QuoteSide{Price: 101 * marketdata.PriceScale, Quantity: &quantity, Time: at, Sign: "0101"},
	}
}
