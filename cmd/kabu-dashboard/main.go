package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/dashboard"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/inspectquality"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/telemetry"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("kabu-dashboard", flag.ContinueOnError)
	fs.SetOutput(out)
	port := fs.Int("port", 18700, "localhostの表示ポート（1024〜65535）")
	dataRoot := fs.String("data-dir", "data", "保存データのディレクトリー")
	demo := fs.Bool("demo", false, "合成データだけを表示（保存データは読み込まない）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("起動引数を確認してください")
	}
	if fs.NArg() != 0 || *port < 1024 || *port > 65535 {
		return errors.New("表示ポートは1024〜65535を指定してください")
	}
	load := func(ctx context.Context, environment string) (dashboard.View, error) {
		now := time.Now().UTC()
		if *demo {
			return dashboard.Demo(environment, now), nil
		}
		view, err := dashboard.Read(ctx, *dataRoot, environment, now)
		if err != nil {
			return view, err
		}
		if view.Files > 0 {
			quality, err := inspectquality.Inspect(ctx, filepath.Join(*dataRoot, environment), environment)
			if err != nil {
				return view, errors.New("品質情報を読み込めません")
			}
			view.Quality = dashboard.Quality{Available: true, NeedsReview: quality.NeedsReview, InvalidFiles: quality.InvalidFiles, MissingPrices: quality.MissingPrices, MissingTimes: quality.MissingMarketTimes, APIErrors: quality.APIErrorRecords}
		}
		return view, nil
	}
	address := "127.0.0.1:" + strconv.Itoa(*port)
	readRuntime := func(environment string) dashboard.Runtime {
		if *demo {
			now := time.Now().UTC()
			return dashboard.Runtime{State: "active", Phase: "waiting", UpdatedAt: &now, StartedAt: &now, Saved: 12, Planned: 60}
		}
		status := telemetry.Observe(*dataRoot, environment, time.Now().UTC())
		return dashboard.Runtime{State: status.State, Phase: status.Phase, UpdatedAt: status.UpdatedAt, StartedAt: status.StartedAt, Saved: status.Saved, Planned: status.Planned}
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return errors.New("表示ポートを開けません。別の-portを指定してください")
	}
	server := &http.Server{Handler: dashboard.Handler(*port, load, readRuntime), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	if _, err := fmt.Fprintf(out, "Dashboard: http://%s/\nRead-only local view. Broker API requests: none.\nStop: Ctrl+C\n", address); err != nil {
		listener.Close()
		return errors.New("起動情報を出力できません")
	}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				_ = server.Close()
			}
		case <-finished:
		}
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("画面サーバーを停止しました")
	}
	return nil
}
