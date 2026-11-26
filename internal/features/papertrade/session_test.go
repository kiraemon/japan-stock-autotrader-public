package papertrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy"
)

type sessionTestGate struct {
	delivery MinuteDelivery
}

func (g *sessionTestGate) Offer(time.Time, time.Time, marketdata.Bar) MinuteDelivery {
	return g.delivery
}

func (*sessionTestGate) Tick(time.Time) MinuteDelivery { return MinuteDelivery{Accepted: true} }

type sessionTestExit struct{}

func (sessionTestExit) OnTimer(time.Time, time.Time) string { return "" }
func (sessionTestExit) OnClose(int64, int64) string         { return "" }

type shortHoldTestExit struct{ sessionTestExit }

func (shortHoldTestExit) OnTimer(now, firstFillAt time.Time) string {
	if now.Before(firstFillAt.Add(5 * time.Second)) {
		return ""
	}
	return "fixture_hold_elapsed"
}

func testQuoteSession(t *testing.T, gate *sessionTestGate) (*QuoteSession, time.Time) {
	t.Helper()
	config := SessionConfig{Date: "2026-06-22", Symbol: "0000@1", Environment: "test", Origin: "synthetic", Direction: strategy.Long}
	session, err := NewQuoteSession(config, "fixture", gate, sessionTestExit{})
	if err != nil {
		t.Fatal(err)
	}
	return session, marketdata.Clock(config.Date, 9, 16)
}

func TestQuoteSessionWindowWithPartialEntryKeepsExposure(t *testing.T) {
	gate := &sessionTestGate{}
	session, at := testQuoteSession(t, gate)
	bar := marketdata.Bar{Start: at.Add(-time.Minute)}
	gate.delivery = MinuteDelivery{
		Accepted: true, Decision: &strategy.Decision{At: at, Accepted: true, Boundary: 100 * marketdata.PriceScale},
	}
	intent := at.Add(2 * time.Second)
	if result := session.Step(intent, SessionInput{Kind: "minute", ReceivedAt: intent, Bar: &bar}); !result.Accepted {
		t.Fatalf("fixture minute rejected: %+v", result)
	}
	q := quoteFixture(intent.Add(time.Second), 40)
	if result := session.Step(q.ReceivedAt, SessionInput{Kind: "quote", Quote: &q}); result.Fill == nil || result.Fill.Delta != 40 {
		t.Fatalf("fixture quote did not partially fill: %+v", result)
	}
	gate.delivery = MinuteDelivery{Accepted: true, Complete: true}
	bar.Start = at
	session.Step(at.Add(time.Minute), SessionInput{Kind: "minute", ReceivedAt: at.Add(time.Minute), Bar: &bar})
	state := session.Snapshot()
	if state.StopReason != "window_ended_with_exposure" || !state.Halted || state.Complete || state.Final.OpenQuantity != 40 ||
		state.Final.ActiveOrders != 1 || state.Final.RemainingQuantity != 60 || state.Final.CleanFinish {
		t.Fatalf("closing the window changed outstanding exposure: %+v", state)
	}
}

func TestQuoteSessionRejectsInvalidDecisionsBeforeRecordingAnOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*strategy.Decision)
	}{
		{"nonpositive boundary", func(d *strategy.Decision) { d.Boundary = 0 }},
		{"accept and stop", func(d *strategy.Decision) { d.Stop = true }},
		{"stop without explanation", func(d *strategy.Decision) { d.Accepted, d.Stop = false, true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &sessionTestGate{}
			session, at := testQuoteSession(t, gate)
			decision := strategy.Decision{At: at, Accepted: true, Boundary: 100 * marketdata.PriceScale}
			tc.check(&decision)
			gate.delivery = MinuteDelivery{Accepted: true, Decision: &decision}
			bar := marketdata.Bar{Start: at.Add(-time.Minute)}
			session.Step(at, SessionInput{Kind: "minute", Bar: &bar})
			state := session.Snapshot()
			if state.StopReason != "strategy_decision_invalid" || state.Signal != nil || len(state.Final.Orders) != 0 {
				t.Fatalf("invalid strategy decision reached the ledger: %+v", state)
			}
		})
	}
}

func TestQuoteRunnerCancellationWhenFinalSnapshotCannotBeStored(t *testing.T) {
	session, at := testQuoteSession(t, &sessionTestGate{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	saves := 0
	state, err := RunQuoteSession(ctx, session, make(chan SessionInput), make(chan time.Time), func() time.Time { return at }, func(s SessionState) error {
		saves++
		if s.StopReason != "context_canceled" || !s.Halted {
			t.Fatalf("stored snapshot did not record the cancellation: %+v", s)
		}
		return errors.New("synthetic storage detail")
	})
	if err == nil || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "synthetic storage detail") ||
		saves != 1 || !state.Halted || state.StopReason != "context_canceled" {
		t.Fatalf("runner did not report the failed terminal save: %v, %+v", err, state)
	}
}

func TestPartialExitUnknownSurvivesRestoreAndDuplicateFill(t *testing.T) {
	const date = "2026-06-22"
	at := marketdata.Clock(date, 9, 16)
	gate := &sessionTestGate{delivery: MinuteDelivery{
		Accepted: true, Decision: &strategy.Decision{At: at, Accepted: true, Boundary: 100 * marketdata.PriceScale},
	}}
	config := SessionConfig{Date: date, Symbol: "0000@1", Environment: "test", Origin: "synthetic", Direction: strategy.Long}
	session, err := NewQuoteSession(config, "fixture", gate, shortHoldTestExit{})
	if err != nil {
		t.Fatal(err)
	}
	observed := at.Add(time.Second)
	bar := marketdata.Bar{Start: at.Add(-time.Minute)}
	if result := session.Step(observed, SessionInput{Kind: "minute", ReceivedAt: observed, Bar: &bar}); !result.Accepted {
		t.Fatalf("fixture entry decision rejected: %+v", result)
	}
	entry := quoteFixture(observed.Add(time.Second), 100)
	if result := session.Step(entry.ReceivedAt, SessionInput{Kind: "quote", Quote: &entry}); result.Fill == nil || !result.Fill.Filled || result.Fill.Delta != 100 {
		t.Fatalf("fixture entry did not fill 100 shares: %+v", result)
	}

	exitAt := entry.ReceivedAt.Add(5 * time.Second)
	if result := session.Step(exitAt, SessionInput{Kind: "tick"}); !result.Accepted || !session.Snapshot().ExitIntentAt.Equal(exitAt) {
		t.Fatalf("fixture timer did not start the exit: %+v", result)
	}
	exit := quoteFixture(exitAt.Add(time.Second), 40)
	if result := session.Step(exit.ReceivedAt, SessionInput{Kind: "quote", Quote: &exit}); result.Fill == nil || !result.Fill.Filled || result.Fill.Delta != 40 {
		t.Fatalf("fixture exit did not fill 40 shares: %+v", result)
	}
	journal := session.Snapshot().Final.Records
	if len(journal) == 0 {
		t.Fatal("partial exit produced no journal records")
	}
	fill := journal[len(journal)-1].Event
	if fill.Kind != "fill" || fill.OrderID != "exit" || fill.CumulativeFilled != 40 {
		t.Fatalf("unexpected recorded exit fill: %+v", fill)
	}

	stoppedAt := exit.ReceivedAt.Add(time.Second)
	session.Step(stoppedAt, SessionInput{Kind: "fault", Fault: "execution_unknown"})
	stopped := session.Snapshot()
	if !stopped.Halted || stopped.StopReason != "execution_unknown" || stopped.Complete {
		t.Fatalf("session did not stop for the unknown exit: %+v", stopped)
	}
	requirePendingExit := func(state Snapshot) {
		t.Helper()
		if state.OpenQuantity != 60 || state.RemainingQuantity != 60 || state.PositionKnown || !state.Halted ||
			!state.ReconcileRequired || state.CleanFinish || state.ActiveOrders != 1 || len(state.Orders) != 2 {
			t.Fatalf("partially filled exit lost its pending exposure: %+v", state)
		}
		order := state.Orders[1]
		if order.ID != "exit" || order.Purpose != "close" || order.State != "unknown" || order.Quantity != 100 || order.Filled != 40 {
			t.Fatalf("wrong retained exit order: %+v", order)
		}
	}
	requirePendingExit(stopped.Final)
	if result := session.Step(stoppedAt.Add(time.Second), SessionInput{Kind: "tick"}); result.Accepted ||
		len(session.Snapshot().Final.Records) != len(stopped.Final.Records) {
		t.Fatalf("stopped session emitted another event: %+v", result)
	}

	recovered := restored(t, session.ledger)
	requirePendingExit(recovered.Snapshot())
	if outcome := recovered.Apply(fill); !outcome.Accepted || !outcome.Duplicate {
		t.Fatalf("recorded exit fill was not recognized as a duplicate: %+v", outcome)
	}
	retry := Event{ID: "retry-exit", At: stoppedAt.Add(time.Second), Kind: "close_intent", OrderID: "another-exit", Quantity: 60}
	if outcome := recovered.Apply(retry); outcome.Accepted || outcome.Reason != "reconciliation_required" {
		t.Fatalf("new exit was allowed before reconciliation: %+v", outcome)
	}
	after := recovered.Snapshot()
	requirePendingExit(after)
	if !slices.Equal(stopped.Final.Orders, after.Orders) || len(after.Records) != len(stopped.Final.Records)+2 {
		t.Fatalf("replay or blocked retry changed the orders or emitted extra events: %+v", after)
	}
}
