package r001

import (
	"testing"
	"time"
)

func TestDirectionAndStrictBoundaries(t *testing.T) {
	if !Crossed(Long, 100, 102, 101) || Crossed(Long, 100, 101, 101) || Crossed(Long, 102, 103, 101) {
		t.Fatal("long crossing")
	}
	if !Crossed(Short, 100, 98, 99) || Crossed(Short, 100, 99, 99) || Crossed(Short, 98, 97, 99) {
		t.Fatal("short crossing")
	}
	if Reversed(Long, 101, 101) || Reversed(Short, 99, 99) || !Reversed(Long, 100, 101) || !Reversed(Short, 100, 99) {
		t.Fatal("reversal boundary")
	}
	for _, tc := range []struct {
		stamp string
		ok    bool
	}{{"2026-10-16T09:14:00+09:00", false}, {"2026-10-16T09:15:00+09:00", true}, {"2026-10-16T10:29:00+09:00", true}, {"2026-10-16T10:30:00+09:00", false}} {
		at, _ := time.Parse(time.RFC3339, tc.stamp)
		if SignalWindow(at) != tc.ok {
			t.Fatal("signal time window")
		}
	}
}
