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
}

// Run collects and saves serially, stopping on any collection or save failure.
// The caller owns the authenticated session and its lifetime.
func Run(ctx context.Context, client Reader, options Options, output io.Writer) error {
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
	return repeat(ctx, options.Count, options.Interval, func() error {
		snapshot, collectErr := Collect(ctx, client, options.Environment, options.Symbols)
		path, err := Save(options.Directory, snapshot)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "保存: %s / complete=%t / records=%d\n", path, snapshot.Complete, len(snapshot.Records)); err != nil {
			return errors.New("保存結果を出力できません")
		}
		return collectErr
	})
}

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
