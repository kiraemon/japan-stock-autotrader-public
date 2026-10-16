package main

import (
	"context"
	"time"
)

// Construct after the initial archive completed. Completion-based scheduling
// skips missed slots; it never catches up with bursts or runs concurrently.
func historyRefresh(every time.Duration, now func() time.Time, capture func(context.Context) error) func(context.Context) error {
	next := now().Add(every)
	var failed error
	return func(ctx context.Context) error {
		if failed != nil {
			return failed
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if now().Before(next) {
			return nil
		}
		if err := capture(ctx); err != nil {
			failed = err
			return err
		}
		next = now().Add(every)
		return nil
	}
}
