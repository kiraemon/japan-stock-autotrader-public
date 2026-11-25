package backtest

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

// Synthetic is an explicit deterministic demonstration, never a market sample.
func Synthetic(direction r001.Direction) marketdata.Dataset {
	const day = "2026-10-14"
	d := marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: "0000@1", Environment: "test", Origin: "synthetic", Converter: "synthetic-fixture-v0.1", Days: []marketdata.Day{{Date: day, Issues: []string{}, Bars: []marketdata.Bar{}, Events: []marketdata.Event{}}}}
	seed := sha256.Sum256([]byte("synthetic-fixture-v0.1/" + string(direction)))
	d.Source = marketdata.Source{Format: "synthetic", SHA256: hex.EncodeToString(seed[:]), Label: "start", ReceivedAt: marketdata.Clock(day, 16, 0)}
	for minute := 0; minute <= 110; minute++ {
		price := int64(100) * marketdata.PriceScale
		if minute >= 15 {
			price = 102 * marketdata.PriceScale
			if direction == r001.Short {
				price = 98 * marketdata.PriceScale
			}
		}
		if minute >= 36 {
			price = 103 * marketdata.PriceScale
			if direction == r001.Short {
				price = 97 * marketdata.PriceScale
			}
		}
		volume := int64(100)
		b := marketdata.Bar{Start: marketdata.Clock(day, 9, 0).Add(time.Duration(minute) * time.Minute), Open: price, High: price, Low: price, Close: price, Volume: &volume, Issues: []string{}}
		if minute < 15 {
			b.High = 101 * marketdata.PriceScale
			b.Low = 99 * marketdata.PriceScale
		}
		d.Days[0].Bars = append(d.Days[0].Bars, b)
	}
	return d
}
