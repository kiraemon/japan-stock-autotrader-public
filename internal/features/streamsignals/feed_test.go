package streamsignals_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/streamsignals"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func TestDemoBothDirectionsAndVariants(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		for _, variant := range []r001.Variant{r001.A, r001.B} {
			_, r, err := streamsignals.Demo(context.Background(), backtest.Synthetic(side), side, variant)
			if err != nil || len(r.Cases) != 14 || r.ForwardTest || r.ExecutionIncluded {
				t.Fatalf("scope/cases: %v", err)
			}
			for _, c := range r.Cases {
				if !c.Passed {
					t.Fatalf("%s/%s/%s: %+v", side, variant, c.Name, c.Final)
				}
			}
			normal := r.Cases[0].Final
			if len(normal.Signals) != 1 || normal.Signals[0].Decision.Accepted != (variant == r001.A) {
				t.Fatal("shared scanner gate differs")
			}
			if !normal.Signals[0].ObservedAt.Equal(normal.Signals[0].Decision.At.Add(2 * time.Second)) {
				t.Fatal("signal backdated to bar close")
			}
		}
	}
}

func fixture() (*streamsignals.Feed, marketdata.Dataset) {
	d := backtest.Synthetic(r001.Long)
	f, _ := streamsignals.New(streamsignals.Config{Date: d.Days[0].Date, Symbol: d.Symbol, Origin: d.Origin, Direction: r001.Long, Variant: r001.B})
	return f, d
}

func TestDeadlineAndClosureBoundaries(t *testing.T) {
	for _, lag := range []time.Duration{2 * time.Second, 10 * time.Second, 10*time.Second + time.Nanosecond} {
		f, d := fixture()
		b := d.Days[0].Bars[0]
		at := b.Start.Add(time.Minute + lag)
		o := f.Offer(at, streamsignals.Observation{ReceivedAt: at, Bar: b})
		if o.Accepted != (lag <= 10*time.Second) {
			t.Fatalf("deadline %v: %+v", lag, o)
		}
	}
	f, d := fixture()
	b := d.Days[0].Bars[0]
	at := b.Start.Add(time.Minute + 2*time.Second)
	if !f.Offer(at, streamsignals.Observation{ReceivedAt: at, Bar: b}).Accepted {
		t.Fatal("first")
	}
	// Repeated old bars cannot postpone the next minute's deadline.
	for _, offset := range []time.Duration{30 * time.Second, 60 * time.Second} {
		if !f.Offer(at.Add(offset), streamsignals.Observation{ReceivedAt: at, Bar: b}).Duplicate {
			t.Fatal("duplicate")
		}
	}
	if f.Tick(b.Start.Add(2*time.Minute+10*time.Second+time.Nanosecond)).Accepted || !f.Snapshot().Halted {
		t.Fatal("duplicate refreshed timeout")
	}
	if f.Offer(at.Add(3*time.Minute), streamsignals.Observation{ReceivedAt: at, Bar: b}).Accepted {
		t.Fatal("halt auto-resumed")
	}
}

func TestSnapshotAndPriorSignalsDoNotAliasInputs(t *testing.T) {
	f, d := fixture()
	quantity := int64(150)
	d.Days[0].Bars[15].Volume = &quantity
	for _, b := range d.Days[0].Bars[:16] {
		at := b.Start.Add(time.Minute + 2*time.Second)
		if !f.Offer(at, streamsignals.Observation{ReceivedAt: at, Bar: b}).Accepted {
			t.Fatal("offer")
		}
	}
	quantity = 0
	s := f.Snapshot()
	if !s.Signals[0].Decision.Accepted || *s.Signals[0].Decision.Volume.SignalVolume != 150 {
		t.Fatal("input mutated prior decision")
	}
	*s.Signals[0].Decision.Volume.SignalVolume = 1
	s.Signals[0].Decision.Volume.Reason = "changed"
	if *f.Snapshot().Signals[0].Decision.Volume.SignalVolume != 150 || f.Snapshot().Signals[0].Decision.Volume.Reason != "" {
		t.Fatal("snapshot alias")
	}
}

func TestInvalidVolumeQualityAndRetainedHistory(t *testing.T) {
	for _, kind := range []string{"nil_volume", "zero_baseline", "quality"} {
		f, d := fixture()
		switch kind {
		case "nil_volume":
			d.Days[0].Bars[15].Volume = nil
		case "zero_baseline":
			for i := 10; i < 15; i++ {
				q := int64(0)
				d.Days[0].Bars[i].Volume = &q
			}
		case "quality":
			d.Days[0].Bars[16].Issues = []string{"unfinished"}
		}
		for _, b := range d.Days[0].Bars[:17] {
			at := b.Start.Add(time.Minute + 2*time.Second)
			f.Offer(at, streamsignals.Observation{ReceivedAt: at, Bar: b})
		}
		if !f.Snapshot().Halted {
			t.Fatal("bad input accepted")
		}
		if kind == "quality" && len(f.Snapshot().Signals) != 1 {
			t.Fatal("stop erased previous signal")
		}
	}
}
