package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestInvalidArgumentsFailBeforeAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-symbols", "7203@1"},
		{"-symbols", "7203@9", "-password-stdin"},
		{"-symbols", "7203@1", "-environment", "other", "-password-stdin"},
		{"-symbols", "7203@1", "-password-stdin", "unexpected"},
		{"-symbols", "7203@1", "-count", "0"},
		{"-symbols", "7203@1", "-count", "10001"},
		{"-symbols", "7203@1", "-interval", "0s"},
		{"-symbols", "7203@1", "-interval", "2h"},
	} {
		var output bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader("test-only-password\n"), &output); err == nil {
			t.Fatalf("expected validation failure for %v", args)
		}
		if strings.Contains(output.String(), "test-only-password") {
			t.Fatal("credential leaked")
		}
	}
}

func TestRepeatCountAndSpacing(t *testing.T) {
	const interval = 5 * time.Millisecond
	var finished time.Time
	calls := 0
	err := repeat(context.Background(), 3, interval, func() error {
		if !finished.IsZero() && time.Since(finished) < interval {
			t.Fatal("next collection started before the post-save interval")
		}
		calls++
		finished = time.Now()
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRepeatStopsOnFailure(t *testing.T) {
	failure := errors.New("collection or save failed")
	calls := 0
	err := repeat(context.Background(), 10, time.Millisecond, func() error {
		calls++
		return failure
	})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRepeatCancellationDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	calls := 0
	go func() {
		done <- repeat(ctx, 3, time.Hour, func() error {
			calls++
			close(started)
			return nil
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the wait")
	}
}

func TestRepeatAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := repeat(ctx, 1, time.Second, func() error {
		t.Fatal("collection started after cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
