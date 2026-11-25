package papertrade

import (
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

func quoteFixture(at time.Time, quantity int64) marketdata.Quote {
	return marketdata.Quote{Version: marketdata.QuoteVersion, Symbol: "0000@1", Environment: "test", Origin: "synthetic", ReceivedAt: at.Add(100 * time.Millisecond), Bid: marketdata.QuoteSide{Price: 99 * marketdata.PriceScale, Quantity: &quantity, Time: at, Sign: "0101"}, Ask: marketdata.QuoteSide{Price: 101 * marketdata.PriceScale, Quantity: &quantity, Time: at, Sign: "0101"}, Issues: []string{}}
}

func quotedLedger(t *testing.T, direction r001.Direction) (*Simulator, *QuoteExecution, time.Time) {
	t.Helper()
	s, _ := New(direction)
	at := marketdata.Clock("2026-10-16", 9, 16).Add(2 * time.Second)
	for _, e := range []Event{{ID: "intent", At: at, Kind: "open_intent", OrderID: "entry", Quantity: 100}, {ID: "accepted", At: at, Kind: "accepted", OrderID: "entry"}} {
		if !s.Apply(e).Accepted {
			t.Fatal("intent")
		}
	}
	return s, NewQuoteExecution(s), at
}

func TestFreshQuoteRoundTripBothDirectionsUsesCorrectSideAndObservationTime(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		s, e, at := quotedLedger(t, side)
		q := quoteFixture(at.Add(time.Second), 100)
		now := q.ReceivedAt.Add(100 * time.Millisecond)
		fill := e.TryFill("entry", now, q)
		want := q.Ask.Price
		if side == r001.Short {
			want = q.Bid.Price
		}
		if !fill.Filled || fill.Price != want || !fill.At.Equal(now) || !fill.At.After(at) || s.Snapshot().OpenQuantity != 100 {
			t.Fatal("entry side/time")
		}
		closeAt := now.Add(time.Second)
		for _, event := range []Event{{ID: "close", At: closeAt, Kind: "close_intent", OrderID: "exit", Quantity: 100}, {ID: "close-accepted", At: closeAt, Kind: "accepted", OrderID: "exit"}} {
			if !s.Apply(event).Accepted {
				t.Fatal("exit intent")
			}
		}
		if got := e.TryFill("exit", closeAt, q); got.Filled || got.Reason != "quote_not_after_intent" {
			t.Fatal("old quote filled exit")
		}
		q = quoteFixture(closeAt.Add(time.Second), 100)
		fill = e.TryFill("exit", q.ReceivedAt, q)
		want = q.Bid.Price
		if side == r001.Short {
			want = q.Ask.Price
		}
		if !fill.Filled || fill.Price != want || !s.Snapshot().CleanFinish {
			t.Fatal("exit side/flat")
		}
	}
}

func TestPartialQuoteCannotBeReusedByReceivingSameBookAgain(t *testing.T) {
	s, e, at := quotedLedger(t, r001.Long)
	q := quoteFixture(at.Add(time.Second), 40)
	if !e.TryFill("entry", q.ReceivedAt, q).Filled || s.Snapshot().OpenQuantity != 40 {
		t.Fatal("partial")
	}
	q.ReceivedAt = q.ReceivedAt.Add(100 * time.Millisecond)
	raw, err := s.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	s, err = Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	e = NewQuoteExecution(s)
	if f := e.TryFill("entry", q.ReceivedAt, q); f.Filled || f.Reason != "quote_already_consumed" || s.Snapshot().OpenQuantity != 40 {
		t.Fatal("liquidity reused")
	}
	q = quoteFixture(at.Add(2*time.Second), 100)
	f := e.TryFill("entry", q.ReceivedAt, q)
	if !f.Filled || f.Delta != 60 || f.Cumulative != 100 || s.Snapshot().OpenQuantity != 100 {
		t.Fatal("new quote remaining quantity")
	}
}

func TestQuoteAverageRoundsConservativelyAtStoredPrecision(t *testing.T) {
	for _, direction := range []r001.Direction{r001.Long, r001.Short} {
		s, e, at := quotedLedger(t, direction)
		q := quoteFixture(at.Add(time.Second), 1)
		q.Bid.Price = 1000001
		q.Ask.Price = 1000003
		if !e.TryFill("entry", q.ReceivedAt, q).Filled {
			t.Fatal("first fill")
		}
		q = quoteFixture(at.Add(2*time.Second), 2)
		q.Bid.Price = 1000002
		q.Ask.Price = 1000004
		f := e.TryFill("entry", q.ReceivedAt, q)
		want := int64(1000004)
		if direction == r001.Short {
			want = 1000001
		}
		if !f.Filled || !f.AverageRounded || s.Snapshot().Orders[0].AveragePrice != want {
			t.Fatal("average precision policy")
		}
	}
}

func TestQuoteExecutionRejectsStaleFutureSpecialAndObservedWithoutLosingOrder(t *testing.T) {
	for _, kind := range []string{"stale", "future_received", "future_market", "special", "missing_qty", "locked", "observed", "before_intent", "halted", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			s, e, at := quotedLedger(t, r001.Long)
			q := quoteFixture(at.Add(time.Second), 100)
			now := q.ReceivedAt
			switch kind {
			case "stale":
				now = now.Add(3 * time.Second)
			case "future_received":
				q.ReceivedAt = now.Add(time.Second)
			case "future_market":
				q.Ask.Time = now.Add(time.Second)
			case "special":
				q.Ask.Sign = "0102"
			case "missing_qty":
				q.Ask.Quantity = nil
			case "locked":
				q.Bid.Price = q.Ask.Price
			case "observed":
				q.Origin = "observed"
			case "before_intent":
				q.Ask.Time = at
			case "halted":
				s.Apply(Event{ID: "stop", At: at, Kind: "stop"})
			case "unknown":
				s.Apply(Event{ID: "unknown", At: at, Kind: "transport_unknown", OrderID: "entry"})
			}
			if f := e.TryFill("entry", now, q); f.Filled || f.Reason == "" || s.Snapshot().OpenQuantity != 0 || s.Snapshot().ActiveOrders != 1 || s.Snapshot().CleanFinish {
				t.Fatal("invalid quote filled or pending order erased")
			}
		})
	}
}
