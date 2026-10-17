package papertrade

import (
	"context"
	"errors"
	"time"
)

// RunQuoteSession owns the only session event loop. Source input must already
// have been saved and timestamped by its producer. A separate ticker wakes the
// loop even if the source never produces another event. No source/credential/API
// is opened here. Blocking storage or an entire hung process needs an external
// watchdog; this loop cannot preempt a blocking callback.
func RunQuoteSession(ctx context.Context, session *QuoteSession, input <-chan SessionInput, ticks <-chan time.Time, now func() time.Time, persist func(SessionState) error) (SessionState, error) {
	if session == nil || input == nil || ticks == nil || now == nil || persist == nil {
		return SessionState{}, errors.New("時刻順runnerの入力・時計・保存処理を確認してください")
	}
	store := func() error {
		if persist(session.Snapshot()) != nil {
			session.Stop(now(), "storage_failure")
			return errors.New("模擬セッションの保存に失敗しました")
		}
		return nil
	}
	for {
		if ctx.Err() != nil {
			session.Stop(now(), "context_canceled")
			if err := store(); err != nil {
				return session.Snapshot(), err
			}
			return session.Snapshot(), ctx.Err()
		}
		if state := session.Snapshot(); state.Halted || state.Complete {
			return state, nil
		}
		select {
		case <-ctx.Done():
			continue
		case _, open := <-ticks:
			if !open {
				session.Stop(now(), "timer_disconnected")
			} else if ctx.Err() == nil {
				// Ignore the ticker payload: it may have waited in a buffer.
				session.Step(now(), SessionInput{Kind: "tick"})
			}
		case event, open := <-input:
			if !open {
				session.Stop(now(), "source_disconnected")
			} else if ctx.Err() == nil {
				// Step checks timeouts before handling queued input, so select
				// ordering cannot rescue a minute whose deadline already passed.
				session.Step(now(), event)
			}
		}
		if ctx.Err() == nil {
			if err := store(); err != nil {
				return session.Snapshot(), err
			}
		}
	}
}
