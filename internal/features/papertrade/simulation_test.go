package papertrade

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/r001"
)

func TestDemoCasesAndReplayAreDeterministic(t *testing.T) {
	r, err := Demo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Cases) != 9 || r.ForwardTest || r.LiveSubmissionCapability {
		t.Fatal("wrong synthetic suite scope")
	}
	unclean := 0
	for _, c := range r.Cases {
		if !c.Passed || !c.CheckpointVerified {
			t.Fatalf("case failed: %s", c.Name)
		}
		if !c.Final.CleanFinish {
			unclean++
		}
	}
	if unclean < 3 {
		t.Fatal("unsafe cases were all turned into normal finishes")
	}
	again, err := Demo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Fatal("same synthetic events changed outcomes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Demo(ctx); err == nil {
		t.Fatal("demo ignored interruption")
	}
}

func event(id, kind, order string, second int) Event {
	return Event{ID: id, Kind: kind, OrderID: order, At: marketdata.Clock("2026-10-16", 9, 16).Add(time.Duration(second) * time.Second)}
}
func apply(t *testing.T, s *Simulator, e Event) {
	t.Helper()
	if out := s.Apply(e); !out.Accepted {
		t.Fatalf("%s: %s", e.Kind, out.Reason)
	}
}
func restored(t *testing.T, s *Simulator) *Simulator {
	t.Helper()
	raw, err := s.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	other, err := Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(s.Snapshot())
	b, _ := json.Marshal(other.Snapshot())
	if !bytes.Equal(a, b) {
		t.Fatal("checkpoint changed ledger")
	}
	return other
}

func TestPartialFillDuringCancellationThenCloseBothDirections(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		s, _ := New(side)
		e := event("entry", "open_intent", "open", 0)
		e.Quantity = 100
		apply(t, s, e)
		apply(t, s, event("ack", "accepted", "open", 1))
		e = event("fill1", "fill", "open", 2)
		e.CumulativeFilled = 40
		e.AveragePrice = 100 * marketdata.PriceScale
		apply(t, s, e)
		apply(t, s, event("cancel", "cancel_requested", "open", 3))
		if snap := s.Snapshot(); snap.OpenQuantity != 40 || snap.RemainingQuantity != 60 || snap.CleanFinish {
			t.Fatal("cancel request cleared residual exposure")
		}
		e = event("fill2", "fill", "open", 4)
		e.CumulativeFilled = 60
		e.AveragePrice = 100 * marketdata.PriceScale
		apply(t, s, e)
		if snap := s.Snapshot(); snap.Orders[0].State != "cancel_pending" || snap.OpenQuantity != 60 || snap.RemainingQuantity != 40 {
			t.Fatal("fill during cancellation lost")
		}
		apply(t, s, event("canceled", "canceled", "open", 5))
		s = restored(t, s)
		if snap := s.Snapshot(); snap.OpenQuantity != 60 || snap.ActiveOrders != 0 || snap.CleanFinish {
			t.Fatal("canceled remainder treated as flat")
		}
		if side == r001.Short && s.Snapshot().SignedQuantity != -60 {
			t.Fatal("short sign lost")
		}
		e = event("exit", "close_intent", "close", 6)
		e.Quantity = 60
		apply(t, s, e)
		e = event("exitfill", "fill", "close", 7)
		e.CumulativeFilled = 60
		e.AveragePrice = 101 * marketdata.PriceScale
		apply(t, s, e)
		if snap := s.Snapshot(); !snap.CleanFinish || snap.OpenQuantity != 0 {
			t.Fatal("known flat ledger not finished")
		}
		restored(t, s)
	}
}

func TestUnknownBlocksEntryThroughRestartAndNeedsExplicitReconciliation(t *testing.T) {
	s, _ := New(r001.Long)
	e := event("entry", "open_intent", "open", 0)
	e.Quantity = 100
	apply(t, s, e)
	apply(t, s, event("timeout", "transport_unknown", "open", 1))
	if snap := s.Snapshot(); snap.PositionKnown || !snap.ReconcileRequired || !snap.Halted || snap.CleanFinish || snap.RemainingQuantity != 100 {
		t.Fatal("unknown treated as flat")
	}
	s = restored(t, s)
	e = event("entry2", "open_intent", "other", 2)
	e.Quantity = 100
	if out := s.Apply(e); out.Accepted || out.Reason != "reconciliation_required" {
		t.Fatal("unknown was automatically retried")
	}
	if out := s.Apply(e); out.Accepted || !out.Duplicate || out.Reason != "reconciliation_required" {
		t.Fatal("rejected duplicate became accepted")
	}
	e = event("reconcile", "reconciled", "open", 3)
	e.ResolvedState = "canceled"
	apply(t, s, e)
	if snap := s.Snapshot(); !snap.CleanFinish || !snap.Halted {
		t.Fatal("reconciliation auto-resumed or failed known-flat close")
	}
	e = event("entry3", "open_intent", "third", 4)
	e.Quantity = 100
	if out := s.Apply(e); out.Accepted || out.Reason != "new_entry_blocked" {
		t.Fatal("recovery automatically resumed entries")
	}
	restored(t, s)
}

func TestLateFillAfterCancellationKeepsExposureUnknownUntilReconciled(t *testing.T) {
	s, _ := New(r001.Short)
	e := event("entry", "open_intent", "open", 0)
	e.Quantity = 100
	apply(t, s, e)
	apply(t, s, event("cancel", "cancel_requested", "open", 1))
	apply(t, s, event("canceled", "canceled", "open", 2))
	e = event("latefill", "fill", "open", 3)
	e.CumulativeFilled = 20
	e.AveragePrice = 100 * marketdata.PriceScale
	apply(t, s, e)
	if snap := s.Snapshot(); snap.OpenQuantity != 20 || snap.PositionKnown || !snap.Halted || snap.CleanFinish {
		t.Fatal("late fill discarded")
	}
	s = restored(t, s)
	e = event("reconcile", "reconciled", "open", 4)
	e.ResolvedState = "canceled"
	e.CumulativeFilled = 20
	e.AveragePrice = 100 * marketdata.PriceScale
	e.ObservedPosition = 0
	if out := s.Apply(e); out.Accepted || out.Reason != "reconciled_position_mismatch" {
		t.Fatal("conflicting observed position accepted")
	}
	e.ID = "reconcile2"
	e.At = e.At.Add(time.Second)
	e.ObservedPosition = 20
	apply(t, s, e)
	e = event("exit", "close_intent", "close", 6)
	e.Quantity = 20
	apply(t, s, e)
	e = event("exitfill", "fill", "close", 7)
	e.CumulativeFilled = 20
	e.AveragePrice = 99 * marketdata.PriceScale
	apply(t, s, e)
	if !s.Snapshot().CleanFinish {
		t.Fatal("reconciled short exposure could not reduce while halted")
	}
}

func TestDuplicateAndConflictingEventsPreserveCumulativeFills(t *testing.T) {
	s, _ := New(r001.Long)
	e := event("entry", "open_intent", "open", 0)
	e.Quantity = 100
	apply(t, s, e)
	f := event("fill", "fill", "open", 1)
	f.CumulativeFilled = 40
	f.AveragePrice = 100 * marketdata.PriceScale
	apply(t, s, f)
	s = restored(t, s)
	if out := s.Apply(f); !out.Accepted || !out.Duplicate || s.Snapshot().OpenQuantity != 40 {
		t.Fatal("fill replay doubled position")
	}
	f.ID = "second_delivery"
	f.At = f.At.Add(time.Second)
	apply(t, s, f)
	if s.Snapshot().OpenQuantity != 40 {
		t.Fatal("same cumulative fill with new event ID doubled position")
	}
	e.ID = "same_intent"
	e.At = f.At.Add(time.Second)
	if out := s.Apply(e); !out.Accepted || !out.Duplicate || len(s.Snapshot().Orders) != 1 {
		t.Fatal("intent replay created another order")
	}
	f.CumulativeFilled = 60
	if out := s.Apply(f); out.Accepted || out.Reason != "event_id_conflict" || s.Snapshot().PositionKnown {
		t.Fatal("conflicting event ID silently overwritten")
	}
	restored(t, s)
}

func TestOverfillStopAndCloseReservationFailClosed(t *testing.T) {
	s, _ := New(r001.Long)
	e := event("entry", "open_intent", "open", 0)
	e.Quantity = 100
	apply(t, s, e)
	f := event("fill", "fill", "open", 1)
	f.CumulativeFilled = 101
	f.AveragePrice = 100 * marketdata.PriceScale
	if out := s.Apply(f); out.Accepted || s.Snapshot().OpenQuantity != 0 || s.Snapshot().PositionKnown || s.Snapshot().CleanFinish {
		t.Fatal("overfill treated as known empty")
	}
	s, _ = New(r001.Long)
	apply(t, s, e)
	f.CumulativeFilled = 100
	apply(t, s, f)
	apply(t, s, event("stop", "stop", "", 2))
	if s.Snapshot().OpenQuantity != 100 || s.Snapshot().CleanFinish {
		t.Fatal("stop erased position")
	}
	x := event("exit", "close_intent", "close1", 3)
	x.Quantity = 60
	apply(t, s, x)
	x = event("exit2", "close_intent", "close2", 4)
	x.Quantity = 60
	if out := s.Apply(x); out.Accepted || out.Reason != "close_exceeds_unreserved_position" {
		t.Fatal("close reservations exceeded position")
	}
	x.ID = "exit3"
	x.OrderID = "close3"
	x.At = x.At.Add(time.Second)
	x.Quantity = 40
	apply(t, s, x)
	if s.Snapshot().RemainingQuantity != 100 {
		t.Fatal("close reservations not retained")
	}
	restored(t, s)
}

func TestCheckpointCorruptionExtraDataAndSnapshotIsolation(t *testing.T) {
	s, _ := New(r001.Long)
	e := event("entry", "open_intent", "open", 0)
	e.Quantity = 100
	apply(t, s, e)
	raw, _ := s.Checkpoint()
	if _, err := Restore(append(raw, []byte(" garbage")...)); err == nil {
		t.Fatal("trailing checkpoint data accepted")
	}
	var c checkpoint
	_ = json.Unmarshal(raw, &c)
	c.Events[0].Quantity = 200
	changed, _ := json.Marshal(c)
	if _, err := Restore(changed); err == nil {
		t.Fatal("modified journal accepted as old state")
	}
	snap := s.Snapshot()
	snap.Orders[0].Quantity = 999
	snap.Records[0].Event.Quantity = 999
	if s.Snapshot().Orders[0].Quantity != 100 || s.Snapshot().Records[0].Event.Quantity != 100 {
		t.Fatal("caller mutated internal ledger")
	}
}

func TestFullFillDuringCancellationAndCumulativeRegression(t *testing.T) {
	s, _ := New(r001.Long)
	e := event("entry", "open_intent", "open", 0)
	e.Quantity = 100
	apply(t, s, e)
	apply(t, s, event("cancel", "cancel_requested", "open", 1))
	f := event("filled", "fill", "open", 2)
	f.CumulativeFilled = 100
	f.AveragePrice = 100 * marketdata.PriceScale
	apply(t, s, f)
	apply(t, s, event("cancel_confirm", "canceled", "open", 3))
	if snap := s.Snapshot(); snap.Orders[0].State != "filled" || snap.OpenQuantity != 100 || snap.CleanFinish {
		t.Fatal("cancel confirmation erased full fill")
	}
	f.ID = "older_fill"
	f.At = f.At.Add(2 * time.Second)
	f.CumulativeFilled = 40
	if out := s.Apply(f); out.Accepted || s.Snapshot().OpenQuantity != 100 || s.Snapshot().PositionKnown {
		t.Fatal("cumulative fill regressed")
	}
	restored(t, s)
}

func TestLateExitFillCannotReleaseConflictingCloseReservations(t *testing.T) {
	s, _ := New(r001.Long)
	e := event("entry", "open_intent", "open", 0)
	e.Quantity = 100
	apply(t, s, e)
	f := event("entryfill", "fill", "open", 1)
	f.CumulativeFilled = 100
	f.AveragePrice = 100 * marketdata.PriceScale
	apply(t, s, f)
	e = event("exit1", "close_intent", "close1", 2)
	e.Quantity = 60
	apply(t, s, e)
	apply(t, s, event("cancel", "cancel_requested", "close1", 3))
	apply(t, s, event("canceled", "canceled", "close1", 4))
	e = event("exit2", "close_intent", "close2", 5)
	e.Quantity = 100
	apply(t, s, e)
	f = event("late", "fill", "close1", 6)
	f.CumulativeFilled = 20
	f.AveragePrice = 100 * marketdata.PriceScale
	apply(t, s, f)
	e = event("reconcile", "reconciled", "close1", 7)
	e.CumulativeFilled = 20
	e.AveragePrice = 100 * marketdata.PriceScale
	e.ObservedPosition = 80
	e.ResolvedState = "canceled"
	if out := s.Apply(e); out.Accepted || out.Reason != "outstanding_close_reservation_conflict" {
		t.Fatal("oversubscribed exit reservation considered reconciled")
	}
	if snap := s.Snapshot(); snap.PositionKnown || snap.OpenQuantity != 80 || snap.CleanFinish {
		t.Fatal("conflicting close reservations ignored")
	}
	restored(t, s)
}
