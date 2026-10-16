package marketdata

import "testing"

func TestDecimalPricesNeverRound(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{{"100.1", 1001000}, {"0.0001", 1}, {"1e2", 1000000}, {"100.0000", 1000000}} {
		got, err := ParsePrice(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("input=%s got=%d err=%v", tc.in, got, err)
		}
	}
	for _, s := range []string{"100.00001", "0", "-1", "NaN", "1/2", "1e999999999", "100000001"} {
		if _, err := ParsePrice(s); err == nil {
			t.Fatalf("invalid price accepted: %s", s)
		}
	}
	if v, err := ParseVolume("100.0"); err != nil || *v != 100 {
		t.Fatal("integer quantity failed")
	}
	if _, err := ParseVolume("0.1"); err == nil {
		t.Fatal("fractional ordinary-share volume accepted")
	}
}
func TestSessionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		h, m    int
		regular bool
	}{{8, 59, false}, {9, 0, true}, {11, 29, true}, {11, 30, false}, {12, 0, false}, {12, 30, true}, {15, 24, true}, {15, 25, false}, {15, 30, false}} {
		if RegularMinute(Clock("2026-10-16", tc.h, tc.m)) != tc.regular {
			t.Fatalf("boundary %02d:%02d", tc.h, tc.m)
		}
	}
}
