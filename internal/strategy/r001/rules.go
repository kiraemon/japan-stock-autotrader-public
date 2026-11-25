// Package r001 defines versioned research rules without files, broker access,
// wall clocks, or execution assumptions.
package r001

import (
	"math/big"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy"
)

type Direction = strategy.Direction
type Variant = strategy.Variant

const (
	Long          = strategy.Long
	Short         = strategy.Short
	A     Variant = "A"
	B     Variant = "B"
)

func ValidVariant(v Variant) bool { return v == A || v == B }
func VersionFor(d Direction, v Variant) string {
	return strings.TrimSuffix(Version(d), "A") + string(v)
}

type VolumeDecision = strategy.VolumeDecision

// VolumeFilter compares integers exactly, including sums larger than int64.
func VolumeFilter(signal *int64, previous []*int64) VolumeDecision {
	r := VolumeDecision{Reason: "volume_input_unavailable"}
	if signal == nil || *signal < 0 || len(previous) != 5 {
		return r
	}
	v := *signal
	r.SignalVolume = &v
	sum := new(big.Int)
	for _, q := range previous {
		if q == nil || *q < 0 {
			return r
		}
		sum.Add(sum, big.NewInt(*q))
	}
	r.PreviousSum = sum.String()
	if sum.Sign() == 0 {
		r.Reason = "volume_baseline_zero"
		return r
	}
	left := new(big.Int).Mul(big.NewInt(v), big.NewInt(10))
	right := new(big.Int).Mul(sum, big.NewInt(3))
	r.Passed = left.Cmp(right) >= 0
	r.Reason = "volume_below_1_5x_previous_5"
	if r.Passed {
		r.Reason = ""
	}
	return r
}

func Valid(d Direction) bool { return strategy.ValidDirection(d) }
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

// SessionExit applies R001's two exit rules to the session's supplied clock
// and completed close price. It returns an empty reason to keep holding.
type SessionExit struct {
	Direction Direction
}

func (SessionExit) OnTimer(now, firstFillAt time.Time) string {
	if !now.Before(firstFillAt.Add(MaximumHold)) {
		return "maximum_hold"
	}
	return ""
}

func (p SessionExit) OnClose(close, boundary int64) string {
	if Reversed(p.Direction, close, boundary) {
		return "range_reversal"
	}
	return ""
}
