package r001

import (
	"math"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func TestVolumeFilterExactThresholdUnknownAndOverflow(t *testing.T) {
	ptr := func(v int64) *int64 { return &v }
	for _, tc := range []struct {
		signal   int64
		previous []*int64
		passed   bool
		reason   string
	}{
		{150, []*int64{ptr(100), ptr(100), ptr(100), ptr(100), ptr(100)}, true, ""},
		{149, []*int64{ptr(100), ptr(100), ptr(100), ptr(100), ptr(100)}, false, "volume_below_1_5x_previous_5"},
		{150, []*int64{ptr(100), ptr(100), nil, ptr(100), ptr(100)}, false, "volume_input_unavailable"},
		{150, []*int64{ptr(0), ptr(0), ptr(0), ptr(0), ptr(0)}, false, "volume_baseline_zero"},
		{math.MaxInt64, []*int64{ptr(math.MaxInt64), ptr(math.MaxInt64), ptr(math.MaxInt64), ptr(math.MaxInt64), ptr(math.MaxInt64)}, false, "volume_below_1_5x_previous_5"},
	} {
		r := VolumeFilter(ptr(tc.signal), tc.previous)
		if r.Passed != tc.passed || r.Reason != tc.reason {
			t.Fatalf("decision=%+v", r)
		}
	}
	if r := VolumeFilter(nil, nil); r.Passed || r.Reason != "volume_input_unavailable" {
		t.Fatal("missing input passed")
	}
}

func scannerBar(m int) marketdata.Bar {
	v := int64(100)
	return marketdata.Bar{Start: marketdata.Clock("2026-10-16", 9, 0).Add(time.Duration(m) * time.Minute), Open: 100, High: 101, Low: 99, Close: 100, Volume: &v}
}

func TestScannerFirstRejectedCandidateConsumesDayAndCopiesInput(t *testing.T) {
	s, _ := NewScanner("2026-10-16", Long, B)
	for m := 0; m < 18; m++ {
		b := scannerBar(m)
		if m == 15 || m == 17 {
			b.Close = 102
			b.High = 102
			v := int64(149)
			if m == 17 {
				v = 1000
			}
			b.Volume = &v
		}
		d, err := s.Observe(b.Start.Add(time.Minute), b)
		if err != nil {
			t.Fatal(err)
		}
		// Future changes through the caller's pointer cannot alter history.
		*b.Volume = 999999
		if m == 15 {
			if d == nil || d.Accepted || d.Reason != "volume_below_1_5x_previous_5" || d.Volume.PreviousSum != "500" {
				t.Fatalf("first candidate=%+v", d)
			}
		} else if d != nil {
			t.Fatal("later candidate was reconsidered")
		}
	}
}

func TestScannerRefusesFutureOutOfOrderAndUncertainMinutesWithoutConsuming(t *testing.T) {
	s, _ := NewScanner("2026-10-16", Long, A)
	b := scannerBar(0)
	if _, err := s.Observe(b.Start, b); err == nil {
		t.Fatal("unfinished minute accepted")
	}
	later := scannerBar(1)
	if _, err := s.Observe(later.Start.Add(time.Minute), later); err == nil {
		t.Fatal("missing first minute accepted")
	}
	b.Issues = []string{"unknown"}
	if _, err := s.Observe(b.Start.Add(time.Minute), b); err == nil {
		t.Fatal("uncertain minute accepted")
	}
	b.Issues = nil
	if _, err := s.Observe(b.Start.Add(time.Minute), b); err != nil {
		t.Fatal("invalid input consumed sequence")
	}
	if _, err := s.Observe(b.Start.Add(time.Minute), b); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := NewScanner("bad", Long, A); err == nil {
		t.Fatal("bad day accepted")
	}
	if _, err := NewScanner("2026-10-16", Long, "C"); err == nil {
		t.Fatal("unimplemented variant accepted")
	}
}
