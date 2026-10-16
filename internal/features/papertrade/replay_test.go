package papertrade_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/papertrade"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func replay(t *testing.T, d marketdata.Dataset, side r001.Direction, v r001.Variant, model papertrade.FillModel) papertrade.ReplayDay {
	t.Helper()
	r, err := papertrade.Replay(context.Background(), d, side, v, model)
	if err != nil {
		t.Fatal(err)
	}
	if r.ForwardTest || r.LiveSubmissionCapability || len(r.Days) != 1 {
		t.Fatal("invalid scope")
	}
	return r.Days[0]
}

func TestStrategyLedgerReplayBothDirectionsAndFillModels(t *testing.T) {
	for _, side := range []r001.Direction{r001.Long, r001.Short} {
		for _, model := range []papertrade.FillModel{papertrade.FullFill, papertrade.PartialFill, papertrade.Unfilled, papertrade.UnknownFill} {
			t.Run(string(side)+"/"+string(model), func(t *testing.T) {
				d := backtest.Synthetic(side)
				r := replay(t, d, side, r001.A, model)
				if r.Decision == nil || !r.Decision.Accepted {
					t.Fatal("signal absent")
				}
				if model == papertrade.UnknownFill {
					if r.Status != "stopped" || r.Final.CleanFinish || r.Final.PositionKnown || !r.Final.Halted || r.Final.ActiveOrders != 1 || len(r.Final.Orders) != 1 {
						t.Fatal("unknown treated as flat or retried")
					}
					return
				}
				if r.Status != "completed" || r.Observed != 111 || !r.Final.CleanFinish {
					t.Fatal("did not finish cleanly")
				}
				if model == papertrade.Unfilled {
					if len(r.Final.Orders) != 1 || r.Final.Orders[0].Filled != 0 || r.Final.Orders[0].State != "canceled" {
						t.Fatal("unfilled opened a position")
					}
				} else {
					quantity := int64(100)
					if model == papertrade.PartialFill {
						quantity = 40
					}
					if len(r.Final.Orders) != 2 || r.Final.Orders[0].Filled != quantity || r.Final.Orders[1].Quantity != quantity || r.Final.Orders[1].Filled != quantity || r.ExitReason != "maximum_hold" {
						t.Fatal("exit did not use filled quantity")
					}
					bt, err := backtest.RunVariant(context.Background(), d, side, r001.A)
					if err != nil {
						t.Fatal(err)
					}
					trade := bt.Days[0].Trade
					if trade == nil {
						t.Fatal("backtest trade absent")
					}
					for _, record := range r.Final.Records {
						if record.Event.Kind != "fill" {
							continue
						}
						at, price := trade.EntryAt, trade.EntryPrice
						if record.Event.OrderID == "exit" {
							at, price = trade.ExitAt, trade.ExitPrice
						}
						if !record.Event.At.Equal(at) || record.Event.AveragePrice != price {
							t.Fatal("next-open price/time differs from fixed backtest")
						}
					}
				}
				raw, _ := json.Marshal(r)
				again, _ := json.Marshal(replay(t, d, side, r001.A, model))
				if !bytes.Equal(raw, again) {
					t.Fatal("replay not deterministic")
				}
			})
		}
	}
}

func TestStrategyLedgerReplayVolumeGate(t *testing.T) {
	d := backtest.Synthetic(r001.Long)
	r := replay(t, d, r001.Long, r001.B, papertrade.FullFill)
	if r.Decision == nil || r.Decision.Accepted || len(r.Final.Orders) != 0 || !r.Final.CleanFinish {
		t.Fatal("filtered signal created order")
	}
	volume := int64(150)
	d.Days[0].Bars[15].Volume = &volume
	r = replay(t, d, r001.Long, r001.B, papertrade.FullFill)
	if !r.Decision.Accepted || len(r.Final.Orders) != 2 {
		t.Fatal("exact volume gate not connected")
	}
	d.Days[0].Bars[15].Volume = nil
	r = replay(t, d, r001.Long, r001.B, papertrade.FullFill)
	if r.Status != "stopped" || r.Reason != "volume_input_unavailable" || len(r.Final.Orders) != 0 {
		t.Fatal("unknown volume treated as filtered/evaluable")
	}
}

func TestStrategyLedgerReplayGapPreservesPositionAndPendingOrder(t *testing.T) {
	d := backtest.Synthetic(r001.Long)
	d.Days[0].Bars = append(d.Days[0].Bars[:17], d.Days[0].Bars[18:]...)
	r := replay(t, d, r001.Long, r001.A, papertrade.PartialFill)
	if r.Status != "stopped" || r.Reason != "missing_minute" || r.Final.OpenQuantity != 40 || r.Final.ActiveOrders != 1 || r.Final.CleanFinish || !r.Final.Halted {
		t.Fatal("gap falsely flattened position")
	}
	d = backtest.Synthetic(r001.Long)
	d.Days[0].Bars = d.Days[0].Bars[:16]
	r = replay(t, d, r001.Long, r001.A, papertrade.FullFill)
	if r.Final.ActiveOrders != 1 || r.Final.CleanFinish || r.Final.OpenQuantity != 0 {
		t.Fatal("missing entry open erased intent")
	}
}

func TestStrategyLedgerReplayOnlyNextOpenCanSetEntryPrice(t *testing.T) {
	d := backtest.Synthetic(r001.Long)
	bar := &d.Days[0].Bars[16]
	bar.Low, bar.Close = 98*marketdata.PriceScale, 98*marketdata.PriceScale
	r := replay(t, d, r001.Long, r001.A, papertrade.PartialFill)
	if r.ExitReason != "range_reversal" || !r.Final.CleanFinish {
		t.Fatal("early reversal not exited")
	}
	for _, rec := range r.Final.Records {
		if rec.Event.Kind == "fill" && rec.Event.OrderID == "entry" && (rec.Event.AveragePrice != 102*marketdata.PriceScale || !rec.Event.At.Equal(bar.Start)) {
			t.Fatal("entry used future close")
		}
		if rec.Event.Kind == "close_intent" && (rec.Event.Quantity != 40 || !rec.Event.At.Equal(marketdata.Clock(d.Days[0].Date, 9, 17))) {
			t.Fatal("early partial exit quantity/time wrong")
		}
	}
	// A reversal whose next open coincides with the deadline creates one exit.
	d = backtest.Synthetic(r001.Long)
	d.Days[0].Bars[35].Low, d.Days[0].Bars[35].Close = 98*marketdata.PriceScale, 98*marketdata.PriceScale
	r = replay(t, d, r001.Long, r001.A, papertrade.FullFill)
	if len(r.Final.Orders) != 2 || r.ExitReason != "maximum_hold" {
		t.Fatal("deadline collision duplicated exit")
	}
}

func TestStrategyLedgerReplayQualityAndObservedDataAreNotPromoted(t *testing.T) {
	d := backtest.Synthetic(r001.Long)
	d.Days[0].Bars[17].Issues = []string{"unfinished"}
	r := replay(t, d, r001.Long, r001.A, papertrade.FullFill)
	if r.Status != "stopped" || r.Final.OpenQuantity != 100 || r.Final.CleanFinish {
		t.Fatal("bad closed bar erased position")
	}
	d.Origin = "observed"
	if _, err := papertrade.Replay(context.Background(), d, r001.Long, r001.A, papertrade.FullFill); err == nil {
		t.Fatal("observed accepted as synthetic")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d = backtest.Synthetic(r001.Long)
	if _, err := papertrade.Replay(ctx, d, r001.Long, r001.A, papertrade.FullFill); err == nil {
		t.Fatal("canceled context ignored")
	}
}
