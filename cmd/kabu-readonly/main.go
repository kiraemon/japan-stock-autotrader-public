package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/collector"
	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("kabu-readonly", flag.ContinueOnError)
	fs.SetOutput(output)
	env := fs.String("environment", "test", "test (18081) または production (18080)")
	symbolList := fs.String("symbols", "", "必須: 7203@1,130A@1 の形式（最大50銘柄）")
	passwordStdin := fs.Bool("password-stdin", false, "APIパスワードを標準入力から1行読み取る")
	count := fs.Int("count", 1, "取得回数（1〜10000）")
	interval := fs.Duration("interval", 10*time.Second, "保存完了から次の取得までの待機時間（1s〜1h）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("不要な引数があります。パスワードは引数に指定しないでください")
	}
	if *count < 1 || *count > 10000 || *interval < time.Second || *interval > time.Hour {
		return errors.New("countは1〜10000、intervalは1s〜1hで指定してください")
	}
	client, err := kabu.New(kabu.Environment(*env))
	if err != nil {
		return err
	}
	defer client.Close()
	var symbols []string
	seen := map[string]bool{}
	for _, item := range strings.Split(*symbolList, ",") {
		symbol := strings.TrimSpace(item)
		if err := kabu.ValidateSymbol(symbol); err != nil {
			return err
		}
		if !seen[symbol] {
			symbols = append(symbols, symbol)
			seen[symbol] = true
		}
	}
	if len(symbols) > 50 {
		return errors.New("銘柄数は最大50です。既存のAPI登録銘柄も確認してください")
	}
	if !*passwordStdin {
		return errors.New("scripts/collect.ps1 を利用するか -password-stdin を指定してください")
	}
	reader := bufio.NewReader(io.LimitReader(input, 1025))
	password, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return errors.New("APIパスワードを読み取れません")
	}
	password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
	if len(password) == 0 || len(password) > 1024 {
		return errors.New("APIパスワードの長さが不正です")
	}
	if err := client.Authenticate(ctx, password); err != nil {
		return err
	}
	password = ""
	return repeat(ctx, *count, *interval, func() error {
		snapshot, collectErr := collector.Collect(ctx, client, kabu.Environment(*env), symbols)
		// Keep all account data in the ignored local data/ tree.
		path, saveErr := collector.Save("data/"+*env, snapshot)
		if saveErr != nil {
			return saveErr
		}
		fmt.Fprintf(output, "保存: %s / complete=%t / records=%d\n", path, snapshot.Complete, len(snapshot.Records))
		return collectErr
	})
}

// Wait after each successful save: slow requests never cause catch-up bursts.
// Authentication is performed once by the caller; any failure ends the run.
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
