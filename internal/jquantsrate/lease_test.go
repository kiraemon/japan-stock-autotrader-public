package jquantsrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseReservesBeforeWorkAndStopsAfterCrashOr429(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate.json")
	l, err := Start(context.Background(), path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), path, time.Minute); err == nil {
		t.Fatal("crashed/overlapping run reservation ignored")
	}
	if err := l.Finish(true); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), path, time.Minute); err == nil {
		t.Fatal("429 cooldown ignored")
	}
	if err := l.Finish(false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var s state
	if json.Unmarshal(raw, &s) != nil || time.Until(s.NotBefore) <= 0 || time.Until(s.NotBefore) > Interval {
		t.Fatal("normal cooldown not persisted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Start(ctx, path, time.Minute); err == nil {
		t.Fatal("cancelled wait continued")
	}
	_ = os.WriteFile(path, []byte("broken"), 0600)
	if _, err := Start(context.Background(), path, time.Minute); err == nil {
		t.Fatal("invalid state accepted")
	}
}
