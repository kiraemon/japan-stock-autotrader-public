package papertrade

import (
	"context"
	"errors"
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
