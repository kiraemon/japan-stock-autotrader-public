package collectquotes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
)

type Options struct {
	Environment kabu.Environment
	Symbols     []string
	Count       int
	Interval    time.Duration
	Directory   string
	Observe     func(Progress) error
	Until       time.Time
	// BeforeCollect runs serially inside this run's cancellation/deadline scope.
	// The caller can compose another read-only capture using the same session.
	BeforeCollect func(context.Context) error
}

// Progress contains metadata only. The caller connects it to shared observation.
type Progress struct {
	Phase    string
	Complete bool
}

// Run collects and saves serially, stopping on any collection or save failure.
// The caller owns the authenticated session and its lifetime.
func Run(ctx context.Context, client Reader, options Options, output io.Writer) (runErr error) {
	if options.Count < 1 || options.Count > 10000 || options.Interval < time.Second || options.Interval > time.Hour {
		return errors.New("countは1〜10000、intervalは1s〜1hで指定してください")
	}
	if options.Environment != kabu.Test && options.Environment != kabu.Production {
		return errors.New("接続環境が不正です")
	}
	if len(options.Symbols) == 0 || len(options.Symbols) > 50 {
		return errors.New("銘柄数は1〜50で指定してください")
	}
	for _, symbol := range options.Symbols {
		if err := kabu.ValidateSymbol(symbol); err != nil {
			return err
		}
	}
	if !options.Until.IsZero() {
		parent := ctx
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadlineCause(ctx, options.Until, errScheduledEnd)
		defer cancel()
		defer func() {
			// Only our scheduled deadline is a successful stop. API timeouts,
			// user cancellation, observer failures and save failures stay errors.
			if errors.Is(runErr, context.DeadlineExceeded) && context.Cause(ctx) == errScheduledEnd && parent.Err() == nil {
				runErr = ErrScheduledEnd
				if _, err := fmt.Fprintln(output, "終了日時に到達したため収集を終了しました"); err != nil {
					runErr = errors.New("終了結果を出力できません")
				}
			}
		}()
	}
	iteration := 0
	notify := func(phase string, complete bool) error {
		if options.Observe != nil {
			return options.Observe(Progress{phase, complete})
		}
		return nil
	}
	return repeat(ctx, options.Count, options.Interval, func() error {
		iteration++
		if err := notify("collecting", false); err != nil {
			return err
		}
		if options.BeforeCollect != nil {
			if err := options.BeforeCollect(ctx); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, collectErr := Collect(ctx, client, options.Environment, options.Symbols)
		path, err := Save(options.Directory, snapshot)
		if err != nil {
			return err
		}
		if err := notify("saved", snapshot.Complete); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "保存: %s / complete=%t / records=%d\n", path, snapshot.Complete, len(snapshot.Records)); err != nil {
			return errors.New("保存結果を出力できません")
		}
		if collectErr != nil {
			return collectErr
		}
		if iteration < options.Count {
			return notify("waiting", false)
		}
		return nil
	})
}

var errScheduledEnd = errors.New("scheduled collection end")

// ErrScheduledEnd reports an intentional stop, possibly with a final partial
// snapshot. The caller records this separately from completing every iteration.
var ErrScheduledEnd = errors.New("指定した終了日時に到達しました")

// Wait after each successful save: slow requests never cause catch-up bursts.
func repeat(ctx context.Context, count int, interval time.Duration, collect func() error) error {
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := collect(); err != nil {
			return err
		}
		if i+1 == count {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
