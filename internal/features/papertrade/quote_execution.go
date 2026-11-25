package papertrade

import (
	"math/big"
	"strconv"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy"
)

const QuoteExecutionVersion = "synthetic-fresh-quote-execution-v0.1"
const SyntheticQuoteMaxAge = 2 * time.Second

type QuoteFill struct {
	Filled         bool      `json:"filled"`
	Reason         string    `json:"reason,omitempty"`
	Delta          int64     `json:"delta_quantity"`
	Cumulative     int64     `json:"cumulative_quantity"`
	Price          int64     `json:"fill_price_scaled"`
	At             time.Time `json:"observed_at"`
	MarketAt       time.Time `json:"quote_market_time"`
	AverageRounded bool      `json:"average_price_rounded"`
}

// QuoteExecution holds a reference to the one synthetic ledger, not a second
// order book. The caller serialises all events. Observed market execution stays
// blocked until a separately validated real-data paper path exists.
type QuoteExecution struct {
	ledger *Simulator
}

func NewQuoteExecution(ledger *Simulator) *QuoteExecution {
	return &QuoteExecution{ledger: ledger}
}

func (e *QuoteExecution) TryFill(orderID string, now time.Time, q marketdata.Quote) QuoteFill {
	r := QuoteFill{At: now}
	block := func(reason string) QuoteFill { r.Reason = reason; return r }
	if e.ledger == nil {
		return block("ledger_unavailable")
	}
	s := e.ledger.Snapshot()
	if !s.PositionKnown || s.Halted {
		return block("ledger_halted_or_unknown")
	}
	if q.Origin != "synthetic" || q.Symbol != "0000@1" || q.Environment != "test" {
		return block("observed_quote_execution_pending")
	}
	var order Order
	var created time.Time
	for _, o := range s.Orders {
		if o.ID == orderID {
			order = o
			break
		}
	}
	for _, record := range s.Records {
		if record.Outcome.Accepted && record.Event.OrderID == orderID && (record.Event.Kind == "open_intent" || record.Event.Kind == "close_intent") {
			created = record.Event.At
			break
		}
	}
	if order.ID == "" || created.IsZero() || (order.State != "working" && order.State != "partially_filled") {
		return block("order_not_working")
	}
	if now.IsZero() || now.Before(created) || q.ReceivedAt.IsZero() || q.ReceivedAt.After(now) {
		return block("invalid_observation_time")
	}
	if q.Version != marketdata.QuoteVersion || len(q.Issues) > 0 || q.Bid.Price <= 0 || q.Ask.Price > marketdata.MaxPrice || q.Bid.Price >= q.Ask.Price || q.Bid.Quantity == nil || q.Ask.Quantity == nil || *q.Bid.Quantity <= 0 || *q.Ask.Quantity <= 0 || q.Bid.Sign != "0101" || q.Ask.Sign != "0101" {
		return block("quote_quality_pending")
	}
	for _, side := range []marketdata.QuoteSide{q.Bid, q.Ask} {
		if side.Time.IsZero() || side.Time.After(q.ReceivedAt) || side.Time.After(now) {
			return block("invalid_quote_time")
		}
		if !side.Time.After(created) || !q.ReceivedAt.After(created) {
			return block("quote_not_after_intent")
		}
		if marketdata.Date(side.Time) != marketdata.Date(created) || !marketdata.RegularMinute(side.Time) {
			return block("quote_outside_session")
		}
		if now.Sub(side.Time) > SyntheticQuoteMaxAge || now.Sub(q.ReceivedAt) > SyntheticQuoteMaxAge {
			return block("stale_quote")
		}
	}
	buy := s.Direction == strategy.Long && order.Purpose == "open" || s.Direction == strategy.Short && order.Purpose == "close"
	side := q.Bid
	if buy {
		side = q.Ask
	}
	// Derive consumed liquidity from the ledger itself, including restored
	// checkpoints. A quote must be newer than the last fill's observation time.
	buyOrders := map[string]bool{}
	for _, prior := range s.Orders {
		buyOrders[prior.ID] = s.Direction == strategy.Long && prior.Purpose == "open" || s.Direction == strategy.Short && prior.Purpose == "close"
	}
	for _, record := range s.Records {
		if record.Event.Kind == "fill" && record.Outcome.Accepted && buyOrders[record.Event.OrderID] == buy && !side.Time.After(record.Event.At) {
			return block("quote_already_consumed")
		}
	}
	delta := order.Quantity - order.Filled
	if *side.Quantity < delta {
		delta = *side.Quantity
	}
	if delta <= 0 {
		return block("no_remaining_quantity")
	}
	// Bound arithmetic by using integers wider than the stored price/quantity.
	numerator := new(big.Int).Mul(big.NewInt(order.AveragePrice), big.NewInt(order.Filled))
	numerator.Add(numerator, new(big.Int).Mul(big.NewInt(side.Price), big.NewInt(delta)))
	average, remainder := new(big.Int), new(big.Int)
	average.QuoRem(numerator, big.NewInt(order.Filled+delta), remainder)
	r.AverageRounded = remainder.Sign() != 0
	// Model assumption: round a buy average up, a sell average down to 1/10000.
	if buy && r.AverageRounded {
		average.Add(average, big.NewInt(1))
	}
	if !average.IsInt64() {
		return block("average_price_overflow")
	}
	fill := Event{ID: "quote-fill-" + strconv.Itoa(len(s.Records)+1), At: now, Kind: "fill", OrderID: orderID, CumulativeFilled: order.Filled + delta, AveragePrice: average.Int64()}
	if outcome := e.ledger.Apply(fill); !outcome.Accepted {
		return block("ledger_fill_rejected")
	}
	r.Filled, r.Delta, r.Cumulative, r.Price, r.MarketAt = true, delta, fill.CumulativeFilled, side.Price, side.Time
	return r
}
