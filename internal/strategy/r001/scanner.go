package r001

import (
	"errors"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy"
)

type Decision = strategy.Decision

// Scanner consumes completed minutes only. It has no future-bar collection,
// orders, account, files, network, or wall clock. First candidate consumes the day.
type Scanner struct {
	direction          Direction
	variant            Variant
	start              time.Time
	seen               int
	boundary, previous int64
	volumes            []*int64
	chosen             bool
	lastObserved       time.Time
}

func NewScanner(day string, direction Direction, variant Variant) (*Scanner, error) {
	date, err := time.ParseInLocation("2006-01-02", day, marketdata.JST)
	if err != nil || date.Format("2006-01-02") != day || !Valid(direction) || !ValidVariant(variant) {
		return nil, errors.New("シグナル判定の日付・方向・版を確認してください")
	}
	s := &Scanner{direction: direction, variant: variant, start: date.Add(9 * time.Hour)}
	if direction == Short {
		s.boundary = marketdata.MaxPrice
	}
	return s, nil
}

func (s *Scanner) Observe(observedAt time.Time, bar marketdata.Bar) (*Decision, error) {
	expected := s.start.Add(time.Duration(s.seen) * time.Minute)
	if s.seen >= 111 || !bar.Start.Equal(expected) || observedAt.Before(bar.Start.Add(time.Minute)) || (!s.lastObserved.IsZero() && observedAt.Before(s.lastObserved)) || len(bar.Issues) > 0 || !marketdata.ValidPrices(bar) || (bar.Volume != nil && *bar.Volume < 0) {
		return nil, errors.New("シグナル入力の順序・確定時刻・価格・品質を確認してください")
	}
	var decision *Decision
	if s.seen < 15 {
		if s.direction == Long && bar.High > s.boundary {
			s.boundary = bar.High
		}
		if s.direction == Short && bar.Low < s.boundary {
			s.boundary = bar.Low
		}
	} else if !s.chosen && SignalWindow(bar.Start) && Crossed(s.direction, s.previous, bar.Close, s.boundary) {
		s.chosen = true
		decision = &Decision{At: bar.Start.Add(time.Minute), Boundary: s.boundary, Accepted: true}
		if s.variant == B {
			volume := VolumeFilter(bar.Volume, s.volumes)
			decision.Volume = &volume
			decision.Accepted = volume.Passed
			decision.Reason = volume.Reason
			decision.Stop = !volume.Passed && volume.Reason != "volume_below_1_5x_previous_5"
		}
	}
	s.previous = bar.Close
	var quantity *int64
	if bar.Volume != nil {
		v := *bar.Volume
		quantity = &v
	}
	s.volumes = append(s.volumes, quantity)
	if len(s.volumes) > 5 {
		s.volumes = s.volumes[1:]
	}
	s.seen++
	s.lastObserved = observedAt
	return decision, nil
}
