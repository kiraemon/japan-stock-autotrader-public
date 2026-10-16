package replaysignals

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func fixture(side r001.Direction) marketdata.Dataset {
	day := "2026-10-16"
	d := marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: "0000@1", Environment: "test", Origin: "synthetic", Converter: "test", Source: marketdata.Source{Format: "synthetic", SHA256: strings.Repeat("a", 64), Label: "start", ReceivedAt: marketdata.Clock(day, 16, 0)}, Days: []marketdata.Day{{Date: day, Issues: []string{}}}}
	for m := 0; m < 111; m++ {
		price := int64(100)
		v := int64(100)
		if m >= 15 {
			price = 102
			if side == r001.Short {
				price = 98
			}
		}
		if m == 15 {
			v = 150
		}
		b := marketdata.Bar{Start: marketdata.Clock(day, 9, 0).Add(time.Duration(m) * time.Minute), Open: price, High: price, Low: price, Close: price, Volume: &v}
		if m < 15 {
			b.High = 101
			b.Low = 99
		}
		d.Days[0].Bars = append(d.Days[0].Bars, b)
	}
	return d
}

func TestReplayUsesClosedMinutesAndDoesNotSeeFuturePrices(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		for _, variant := range []r001.Variant{r001.A, r001.B} {
			d := fixture(side)
			r, err := Run(context.Background(), d, side, variant)
			if err != nil {
				t.Fatal(err)
			}
			day := r.Days[0]
			if r.ForwardTest || r.ExecutionIncluded || r.StrategyAdopted || day.Status != "completed" || day.Observed != 111 || len(day.Events) != 1 || !day.Events[0].Decision.Accepted || !day.Events[0].ObservedAt.Equal(marketdata.Clock(day.Date, 9, 16)) {
				t.Fatalf("unexpected replay: %+v", r)
			}
			before := day.Events
			for i := 16; i < len(d.Days[0].Bars); i++ {
				b := &d.Days[0].Bars[i]
				b.Open = 500
				b.High = 500
				b.Low = 500
				b.Close = 500
				v := int64(999999)
				b.Volume = &v
			}
			after, err := Run(context.Background(), d, side, variant)
			if err != nil || !reflect.DeepEqual(before, after.Days[0].Events) {
				t.Fatal("future input changed earlier decision")
			}
		}
	}
}

func TestReplaySeparatesIncompleteUnknownAndObservedInputs(t *testing.T) {
	d := fixture(r001.Long)
	d.Source.ReceivedAt = marketdata.Clock("2026-10-16", 9, 15).Add(30 * time.Second)
	r, err := Run(context.Background(), d, r001.Long, r001.A)
	if err != nil || r.Days[0].Status != "unevaluable" || r.Days[0].Observed != 15 || len(r.Days[0].Events) != 0 {
		t.Fatal("unfinished minute affected signal")
	}
	d = fixture(r001.Long)
	d.Origin = "observed"
	d.Days[0].Issues = nil
	r, err = Run(context.Background(), d, r001.Long, r001.A)
	if err != nil || r.Days[0].Status != "unevaluable" || r.Days[0].Observed != 0 {
		t.Fatal("missing observed quality bypassed")
	}
	d = fixture(r001.Long)
	d.Days[0].Bars = d.Days[0].Bars[1:]
	r, err = Run(context.Background(), d, r001.Long, r001.A)
	if err != nil || r.Days[0].Status != "unevaluable" || r.Days[0].Observed != 0 {
		t.Fatal("missing opening minute filled")
	}
	d = fixture(r001.Long)
	d.Days[0].Bars[15].Volume = nil
	r, err = Run(context.Background(), d, r001.Long, r001.B)
	if err != nil || r.Days[0].Status != "unevaluable" || len(r.Days[0].Events) != 1 || r.Days[0].Events[0].Decision.Accepted {
		t.Fatal("unknown volume treated as a valid filter rejection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, d, r001.Long, r001.A); err == nil {
		t.Fatal("cancel ignored")
	}
}
