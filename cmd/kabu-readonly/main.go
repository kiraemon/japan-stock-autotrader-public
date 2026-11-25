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

	"github.com/kiraemon/japan-stock-trading-lab/internal/credentials"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/capturehistory"
	"github.com/kiraemon/japan-stock-trading-lab/internal/features/collectquotes"
	"github.com/kiraemon/japan-stock-trading-lab/internal/kabu"
	"github.com/kiraemon/japan-stock-trading-lab/internal/telemetry"
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
	return runWithSessionClock(ctx, args, input, output, openSession, capturehistory.WallClock{})
}

func runWithSessionClock(ctx context.Context, args []string, input io.Reader, output io.Writer, openSession func(kabu.Environment) (session, error), clock capturehistory.Clock) (runErr error) {
	fs := flag.NewFlagSet("kabu-readonly", flag.ContinueOnError)
	fs.SetOutput(output)
	env := fs.String("environment", "test", "test (18081) または production (18080)")
	symbolList := fs.String("symbols", "", "必須: 7203@1,130A@1 の形式（最大50銘柄）")
	passwordStdin := fs.Bool("password-stdin", false, "APIパスワードを標準入力から1行読み取る")
	onePassword := fs.Bool("onepassword", false, "暗号化済みサービスアカウント設定からAPIパスワードを取得")
	count := fs.Int("count", 1, "取得回数（1〜10000）")
	interval := fs.Duration("interval", 10*time.Second, "保存完了から次の取得までの待機時間（1s〜1h）")
	untilText := fs.String("until", "", "終了日時: RFC3339（例 2026-10-16T15:35:00+09:00）。countと早い方で終了")
	historyList := fs.String("history-symbols", "", "開始時に一度だけ歩み値を保存する銘柄（最大10、例7203@1,8306@1）")
	historyEvery := fs.Duration("history-every", 0, "歩み値保存完了から再取得までの最小間隔（0で起動時のみ、1m〜1h）")
	minuteDate := fs.String("history-minute-date", "", "JST日付の09:00〜10:50足を終了+2秒に原保存。1〜3銘柄、111回、通常保存は各回の後")
	minuteAudit := fs.Bool("history-minute-audit", false, "毎分履歴の原保存後に分足候補を検査し、検査保存まで同じ10秒締切に含める。戦略供給はしない")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("不要な引数があります。パスワードは引数に指定しないでください")
	}
	specified := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { specified[f.Name] = true })
	if *minuteAudit && *minuteDate == "" {
		return errors.New("history-minute-auditにはhistory-minute-dateが必要です")
	}
	if *count < 1 || *count > 10000 || *interval < time.Second || *interval > time.Hour {
		return errors.New("countは1〜10000、intervalは1s〜1hで指定してください")
	}
	if *historyEvery != 0 && (*historyEvery < time.Minute || *historyEvery > time.Hour || *historyList == "") {
		return errors.New("history-everyはhistory-symbolsと合わせて1m〜1hで指定してください（0は起動時のみ）")
	}
	var until time.Time
	if *untilText != "" {
		var err error
		until, err = time.Parse(time.RFC3339, *untilText)
		if err != nil || !until.After(clock.Now()) || until.Sub(clock.Now()) > 24*time.Hour {
			return errors.New("untilは24時間以内の未来の日時をタイムゾーン付きRFC3339で指定してください")
		}
		countSpecified := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "count" {
				countSpecified = true
			}
		})
		if !countSpecified {
			*count = 10000
		}
	}
	if *minuteDate != "" && (specified["count"] || specified["interval"] || specified["history-every"]) {
		return errors.New("history-minute-dateはcount・interval・history-everyと同時指定できません")
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
	var historySymbols []string
	if *historyList != "" {
		historySeen := map[string]bool{}
		for _, item := range strings.Split(*historyList, ",") {
			symbol := strings.TrimSpace(item)
			if err := kabu.ValidateSymbol(symbol); err != nil {
				return err
			}
			if !historySeen[symbol] {
				historySymbols = append(historySymbols, symbol)
				historySeen[symbol] = true
				seen[symbol] = true
			}
		}
		if len(historySymbols) > 10 || len(seen) > 50 {
			return errors.New("履歴は最大10銘柄、通常収集との合計は最大50銘柄です")
		}
	}
	if *minuteDate != "" {
		if err := capturehistory.ValidateAligned(*minuteDate, historySymbols, clock.Now()); err != nil {
			return err
		}
		if len(symbols) != len(historySymbols) {
			return errors.New("毎分履歴ではsymbolsとhistory-symbolsを同じ銘柄にしてください")
		}
		for _, symbol := range symbols {
			found := false
			for _, historySymbol := range historySymbols {
				found = found || historySymbol == symbol
			}
			if !found {
				return errors.New("毎分履歴ではsymbolsとhistory-symbolsを同じ銘柄にしてください")
			}
		}
		*count = capturehistory.AlignedMinutes
		if until.IsZero() || until.After(capturehistory.AlignedEnd(*minuteDate)) {
			until = capturehistory.AlignedEnd(*minuteDate)
		}
	}
	if *passwordStdin == *onePassword {
		return errors.New("-password-stdin または -onepassword のどちらか1つを指定してください")
	}
	var processRound func(context.Context, capturehistory.AlignedRound) (string, error)
	if *minuteAudit {
		var err error
		processRound, err = newMinuteAudit(*minuteDate, *env, historySymbols, "data/history/"+*env, clock.Now)
		if err != nil {
			return err
		}
	}
	client, err := openSession(kabu.Environment(*env))
	if err != nil {
		return err
	}
	defer client.Close()
	var historyReader capturehistory.Reader
	if len(historySymbols) > 0 {
		var ok bool
		historyReader, ok = client.(capturehistory.Reader)
		if !ok {
			return errors.New("履歴取得に対応していない接続です")
		}
	}
	monitor, err := telemetry.Start(ctx, "data", *env, *count)
	if err != nil {
		return err
	}
	defer func() {
		state := "completed"
		if errors.Is(runErr, collectquotes.ErrScheduledEnd) {
			state = "scheduled_end"
			runErr = nil
		}
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
	if !until.IsZero() && !until.After(clock.Now()) {
		return errors.New("終了日時を過ぎたため認証を開始しません")
	}
	if *minuteDate != "" {
		if err := capturehistory.ValidateAligned(*minuteDate, historySymbols, clock.Now()); err != nil {
			return err
		}
	}
	if err := monitor.Phase("authenticating"); err != nil {
		return err
	}
	if err := client.Authenticate(workCtx, password); err != nil {
		return err
	}
	password = ""
	if *minuteDate != "" {
		if _, err := fmt.Fprintf(output, "毎分履歴モード: version=%s / date=%s / rounds=%d / offset=%s / raw_deadline=%s / until=%s\nconversion_included=%t / strategy_eligible=false\n", capturehistory.AlignedVersion, *minuteDate, capturehistory.AlignedMinutes, capturehistory.AlignedOffset, capturehistory.AlignedDeadline, until.Format(time.RFC3339), *minuteAudit); err != nil {
			return errors.New("毎分履歴の開始結果を出力できません")
		}
		alignedCtx, cancel := context.WithDeadlineCause(workCtx, until, errAlignedEnd)
		defer cancel()
		err := capturehistory.RunAligned(alignedCtx, historyReader, capturehistory.AlignedOptions{
			Date: *minuteDate, Environment: kabu.Environment(*env), Symbols: historySymbols, Directory: "data/history/" + *env, Clock: clock,
			Observe:      monitor.Phase,
			ProcessRound: processRound,
			AfterRound: func(ctx context.Context) error {
				return collectquotes.Run(ctx, client, collectquotes.Options{Environment: kabu.Environment(*env), Symbols: symbols, Count: 1, Interval: time.Second, Directory: "data/" + *env, Observe: func(p collectquotes.Progress) error {
					if p.Phase == "saved" {
						return monitor.Saved(p.Complete)
					}
					return monitor.Phase(p.Phase)
				}}, output)
			},
		}, output)
		if errors.Is(err, context.DeadlineExceeded) && context.Cause(alignedCtx) == errAlignedEnd && workCtx.Err() == nil {
			if _, writeErr := fmt.Fprintln(output, "終了日時に到達したため毎分履歴収集を終了しました"); writeErr != nil {
				return errors.New("終了結果を出力できません")
			}
			return collectquotes.ErrScheduledEnd
		}
		return err
	}
	if len(historySymbols) > 0 {
		if err := monitor.Phase("collecting"); err != nil {
			return err
		}
		historyCtx := workCtx
		if !until.IsZero() {
			var cancel context.CancelFunc
			historyCtx, cancel = context.WithDeadline(workCtx, until)
			defer cancel()
		}
		if err := capturehistory.Capture(historyCtx, historyReader, kabu.Environment(*env), historySymbols, "data/history/"+*env, output); err != nil {
			return err
		}
	}
	if !until.IsZero() && !until.After(time.Now()) {
		return errors.New("終了日時を過ぎたため収集を開始しません")
	}
	if !until.IsZero() {
		if _, err := fmt.Fprintf(output, "収集終了日時: %s / 最大%d回 / 保存後待機=%s\n", until.In(time.FixedZone("JST", 9*60*60)).Format(time.RFC3339), *count, *interval); err != nil {
			return errors.New("開始結果を出力できません")
		}
	}
	var beforeCollect func(context.Context) error
	if *historyEvery > 0 {
		beforeCollect = historyRefresh(*historyEvery, time.Now, func(ctx context.Context) error {
			return capturehistory.Capture(ctx, historyReader, kabu.Environment(*env), historySymbols, "data/history/"+*env, output)
		})
	}
	return collectquotes.Run(workCtx, client, collectquotes.Options{
		Environment: kabu.Environment(*env), Symbols: symbols,
		Count: *count, Interval: *interval, Directory: "data/" + *env,
		Until:         until,
		BeforeCollect: beforeCollect,
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
var errAlignedEnd = errors.New("aligned capture scheduled end")

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
