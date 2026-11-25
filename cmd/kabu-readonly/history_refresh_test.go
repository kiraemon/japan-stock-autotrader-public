package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/kabu"
)

func TestHistoryRefreshCompletionSpacingNoCatchupOrRetry(t *testing.T) {
	at := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	calls := 0
	refresh := historyRefresh(time.Minute, func() time.Time { return at }, func(context.Context) error { calls++; at = at.Add(7 * time.Second); return nil })
	ctx := context.Background()
	refresh(ctx)
	at = at.Add(59 * time.Second)
	refresh(ctx)
	if calls != 0 {
		t.Fatal("bootstrap duplicated")
	}
	at = at.Add(time.Second)
	refresh(ctx)
	if calls != 1 {
		t.Fatal("due archive not captured")
	}
	at = at.Add(59 * time.Second)
	refresh(ctx)
	if calls != 1 {
		t.Fatal("post-completion spacing violated")
	}
	at = at.Add(5 * time.Minute)
	refresh(ctx)
	refresh(ctx)
	if calls != 2 {
		t.Fatal("missed slots produced burst")
	}
	failure := errors.New("synthetic capture failure")
	refresh = historyRefresh(time.Minute, func() time.Time { return at }, func(context.Context) error { calls++; return failure })
	at = at.Add(time.Minute)
	if !errors.Is(refresh(ctx), failure) || !errors.Is(refresh(ctx), failure) || calls != 3 {
		t.Fatal("failure retried or lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refresh = historyRefresh(time.Minute, func() time.Time { return at }, func(context.Context) error { t.Fatal("capture after cancellation"); return nil })
	if !errors.Is(refresh(ctx), context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

func TestHistoryRefreshArgumentsFailBeforeSessionOrPassword(t *testing.T) {
	for _, args := range [][]string{
		{"-symbols", "7203@1", "-password-stdin", "-history-every", "1m"},
		{"-symbols", "7203@1", "-history-symbols", "7203@1", "-password-stdin", "-history-every", "59s"},
		{"-symbols", "7203@1", "-history-symbols", "7203@1", "-password-stdin", "-history-every", "2h"},
	} {
		err := runWithSession(context.Background(), args, unreadableInput{t}, &bytes.Buffer{}, func(kabu.Environment) (session, error) { t.Fatal("session opened before validation"); return nil, nil })
		if err == nil {
			t.Fatal("invalid schedule accepted")
		}
	}
}
