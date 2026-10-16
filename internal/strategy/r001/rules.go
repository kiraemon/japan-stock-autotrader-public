// Package r001 defines versioned research rules without files, broker access,
// wall clocks, or execution assumptions.
package r001

import "time"

type Direction string

const (
	Long  Direction = "long"
	Short Direction = "short"
)

func Valid(d Direction) bool { return d == Long || d == Short }
func Version(d Direction) string {
	if d == Short {
		return "R001-S-v0.1-A"
	}
	return "R001-v0.1-A"
}
func Crossed(d Direction, previous, close, boundary int64) bool {
	if d == Short {
		return previous >= boundary && close < boundary
	}
	return previous <= boundary && close > boundary
}
func Reversed(d Direction, close, boundary int64) bool {
	if d == Short {
		return close > boundary
	}
	return close < boundary
}
func SignalWindow(start time.Time) bool {
	t := start.In(time.FixedZone("JST", 9*3600))
	minute := t.Hour()*60 + t.Minute()
	return minute >= 9*60+15 && minute < 10*60+30
}

const MaximumHold = 20 * time.Minute
