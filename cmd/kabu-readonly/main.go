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

	"github.com/kiraemon/japan-stock-autotrader/internal/credentials"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/collectquotes"
	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
	"github.com/kiraemon/japan-stock-autotrader/internal/telemetry"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type session interface {
	collectquotes.Reader
	Authenticate(context.Context, string) error
	Close()
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	return runWithSession(ctx, args, input, output, func(env kabu.Environment) (session, error) {
		return kabu.Open(env)
	})
}

func runWithSession(ctx context.Context, args []string, input io.Reader, output io.Writer, openSession func(kabu.Environment) (session, error)) (runErr error) {
	fs := flag.NewFlagSet("kabu-readonly", flag.ContinueOnError)
	fs.SetOutput(output)
	env := fs.String("environment", "test", "test (18081) または production (18080)")
	symbolList := fs.String("symbols", "", "必須: 7203@1,130A@1 の形式（最大50銘柄）")
	passwordStdin := fs.Bool("password-stdin", false, "APIパスワードを標準入力から1行読み取る")
	onePassword := fs.Bool("onepassword", false, "暗号化済みサービスアカウント設定からAPIパスワードを取得")
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
	if *passwordStdin == *onePassword {
		return errors.New("-password-stdin または -onepassword のどちらか1つを指定してください")
	}
	client, err := openSession(kabu.Environment(*env))
	if err != nil {
		return err
	}
	defer client.Close()
	monitor, err := telemetry.Start(ctx, "data", *env, *count)
	if err != nil {
		return err
	}
	defer func() {
		state := "completed"
		if monitor.Err() != nil {
			runErr = monitor.Err()
			state = "failed"
		} else if runErr != nil {
			state = "failed"
			if ctx.Err() != nil || errors.Is(runErr, errInputClosed) {
				state = "interrupted"
			}
		}
		if err := monitor.Finish(state); err != nil {
			runErr = err
		}
	}()
	workCtx := monitor.Context()
	if err := monitor.Phase("credentials"); err != nil {
		return err
	}
	var password string
	if *onePassword {
		password, err = credentials.OnePassword(workCtx, ".", *env)
		if err != nil {
			return err
		}
	} else {
		password, err = readPassword(workCtx, input)
		if err != nil {
			return err
		}
	}
	if len(password) == 0 || len(password) > 1024 {
		return errors.New("APIパスワードの長さが不正です")
	}
	if err := workCtx.Err(); err != nil {
		return err
	}
	if err := monitor.Phase("authenticating"); err != nil {
		return err
	}
	if err := client.Authenticate(workCtx, password); err != nil {
		return err
	}
	password = ""
	return collectquotes.Run(workCtx, client, collectquotes.Options{
		Environment: kabu.Environment(*env), Symbols: symbols,
		Count: *count, Interval: *interval, Directory: "data/" + *env,
		Observe: func(p collectquotes.Progress) error {
			if p.Phase == "saved" {
				return monitor.Saved(p.Complete)
			}
			return monitor.Phase(p.Phase)
		},
	}, output)
}

// Input can block while the user is away. Cancellation must still release the
// startup guard and finish status; a blocked input goroutine ends with the process.
var errInputClosed = errors.New("APIパスワードの入力を中断しました")

func readPassword(ctx context.Context, input io.Reader) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	type result struct {
		password string
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(io.LimitReader(input, 1025))
		password, err := reader.ReadString('\n')
		// Windows console Ctrl+C can finish ReadConsole as empty EOF before the
		// signal notification goroutine runs. Closed input is an aborted start.
		if err == io.EOF && password == "" {
			resultCh <- result{"", errInputClosed}
			return
		}
		if err != nil && err != io.EOF {
			resultCh <- result{"", errors.New("APIパスワードを読み取れません")}
			return
		}
		resultCh <- result{strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r"), nil}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-resultCh:
		return value.password, value.err
	}
}
