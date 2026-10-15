package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/inspectquality"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	fs := flag.NewFlagSet("kabu-inspect", flag.ContinueOnError)
	fs.SetOutput(output)
	environment := fs.String("environment", "production", "保存データの環境: production または test（API通信なし）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*environment != "production" && *environment != "test") {
		return errors.New("-environment production または testを指定してください")
	}
	report, err := inspectquality.Inspect(ctx, "data/"+*environment, *environment)
	if err != nil {
		return err
	}
	path, err := inspectquality.Save("data/quality", report)
	if err != nil {
		return err
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "集計: environment=%s / files=%d / valid=%d / incomplete=%d\n", report.Environment, report.Files, report.ValidFiles, report.IncompleteFiles)
	fmt.Fprintf(&summary, "構造: invalid=%d / missing_apis=%d / api_errors=%d / invalid_data=%d\n", report.InvalidFiles, report.FilesMissingRequiredAPIs, report.APIErrorRecords, report.InvalidDataRecords)
	fmt.Fprintf(&summary, "板: records=%d / missing_price=%d / missing_time=%d / repeated_time=%d\n", report.BoardRecords, report.MissingPrices, report.MissingMarketTimes, report.RepeatedMarketTimes)
	fmt.Fprintf(&summary, "時刻: invalid_receive=%d / backwards_market=%d / future_market=%d / overlapping=%d\n", report.InvalidReceiveTimes, report.BackwardMarketTimes, report.MarketTimeAheadOfReceive, report.OverlappingSnapshots)
	if report.PostCollectionGap.Count > 0 {
		fmt.Fprintf(&summary, "取得完了→次回開始（秒）: min=%.3f / max=%.3f / count=%d\n", report.PostCollectionGap.Min, report.PostCollectionGap.Max, report.PostCollectionGap.Count)
	}
	if report.PriceAgeAtReceive.Count > 0 {
		fmt.Fprintf(&summary, "受信時点の価格経過（秒）: min=%.3f / max=%.3f / count=%d\n", report.PriceAgeAtReceive.Min, report.PriceAgeAtReceive.Max, report.PriceAgeAtReceive.Count)
	}
	status := "指摘なし"
	if report.NeedsReview {
		status = "要確認"
	}
	fmt.Fprintf(&summary, "判定: %s（保存データの品質のみ。休場判定・鮮度の合否判定は未実施）\n保存: %s\n", status, path)
	if _, err := io.WriteString(output, summary.String()); err != nil {
		return errors.New("品質結果を出力できません")
	}
	return nil
}
