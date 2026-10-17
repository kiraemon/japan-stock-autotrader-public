package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/comparebars"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
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
	if ctx.Err() != nil {
		return errors.New("研究処理を中断しました")
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(out, "kabu-research demo | build-bars | compare-bars | diagnose | replay | paper-demo | paper-replay | feed-demo | quote-demo | quote-check | history-feed-check | session-demo | capital-check | backtest（オフライン専用・発注機能なし）。各コマンドに-hで説明。")
		return err
	}
	command := args[0]
	if command == "capital-check" {
		return runCapitalCheck(ctx, args[1:], out)
	}
	if command != "demo" && command != "build-bars" && command != "compare-bars" && command != "diagnose" && command != "backtest" && command != "replay" && command != "paper-demo" && command != "paper-replay" && command != "feed-demo" && command != "quote-demo" && command != "quote-check" && command != "history-feed-check" && command != "session-demo" {
		return errors.New("demo / build-bars / compare-bars / diagnose / replay / paper-demo / paper-replay / feed-demo / quote-demo / quote-check / history-feed-check / session-demo / backtestを指定してください")
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "元JSONまたはCSVのパス（値はログへ表示しない）")
	reference := fs.String("reference", "", "照合先分足JSON / 元歩み値JSON / history-feed-checkの2番目の履歴")
	format := fs.String("format", "history", "build-barsの入力: history / csv")
	symbol := fs.String("symbol", "", "CSVの銘柄・市場（例7203@1）")
	environment := fs.String("environment", "production", "CSVの環境: production / test")
	label := fs.String("csv-label", "", "CSVの時刻ラベル: start / end（必須、取得元の仕様を確認）")
	captured := fs.String("captured-at", "", "CSV取得日時: RFC3339（CSV時刻の意味は別途確認）")
	direction := fs.String("direction", "long", "研究方向: long / short（別仮説として集計）")
	variant := fs.String("variant", "A", "研究ルール: A / B（Bは直前5本の平均出来高の1.5倍以上）")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("不要な引数があります")
	}
	if !r001.Valid(r001.Direction(*direction)) {
		return errors.New("directionはlongまたはshortです")
	}
	if !r001.ValidVariant(r001.Variant(*variant)) {
		return errors.New("variantはAまたはBです")
	}
	if command == "history-feed-check" {
		if *captured != "" || *label != "" {
			return errors.New("history-feed-checkは保存済み履歴の取得・完了時刻だけを使います")
		}
		return historyFeedCheck(ctx, out, *input, *reference, *symbol, *environment)
	}
	if command == "quote-check" {
		if *reference != "" || *captured != "" {
			return errors.New("quote-checkはスナップショットのinputだけを使います")
		}
		return quoteCheck(ctx, out, *input, *symbol, *environment)
	}
	if command == "paper-demo" || command == "paper-replay" || command == "feed-demo" || command == "quote-demo" || command == "session-demo" {
		if *input != "" || *reference != "" || *captured != "" || *symbol != "" {
			return errors.New("paper-demo / paper-replay / feed-demo / quote-demo / session-demoは組込の合成入力だけを使います")
		}
		if command == "session-demo" {
			return sessionDemo(ctx, out, r001.Direction(*direction), r001.Variant(*variant))
		}
		if command == "quote-demo" {
			return quoteDemo(ctx, out, r001.Direction(*direction))
		}
		if command == "feed-demo" {
			return feedDemo(ctx, out, r001.Direction(*direction), r001.Variant(*variant))
		}
		if command == "paper-replay" {
			return paperReplay(ctx, out, r001.Direction(*direction), r001.Variant(*variant))
		}
		return paperDemo(ctx, out)
	}
	if command == "diagnose" {
		return diagnose(ctx, out, diagnosticOptions{Input: *input, Reference: *reference, Symbol: *symbol, Environment: *environment, Label: *label, Captured: *captured, Direction: r001.Direction(*direction), Variant: r001.Variant(*variant)})
	}
	var dataset marketdata.Dataset
	var raw []byte
	var err error
	if command == "demo" || (command == "replay" && *input == "") {
		if *input != "" {
			return errors.New("demoは実ファイルを入力にしません")
		}
		dataset = backtest.Synthetic(r001.Direction(*direction))
		raw, _ = json.Marshal(dataset)
	} else {
		if *input == "" {
			return errors.New("-inputに入力ファイルを指定してください")
		}
		raw, err = readInput(*input)
		if err != nil {
			return err
		}
		if command == "build-bars" {
			switch *format {
			case "history":
				dataset, err = buildbars.FromHistory(ctx, raw)
			case "csv":
				at, parseErr := time.Parse(time.RFC3339, *captured)
				if parseErr != nil {
					return errors.New("CSV取得日時をRFC3339で指定してください")
				}
				dataset, err = buildbars.FromCSV(ctx, raw, *symbol, *environment, *label, at)
			default:
				return errors.New("formatはhistoryまたはcsvです")
			}
			if err != nil {
				return err
			}
			path, outputRaw, err := save("bars", dataset)
			if err != nil {
				return err
			}
			settings := struct {
				Converter, Symbol, Environment string
				Source                         marketdata.Source
			}{dataset.Converter, dataset.Symbol, dataset.Environment, dataset.Source}
			manifest, err := recordRun(command, filepath.Base(*input), raw, dataset.Source.SHA256, path, outputRaw, settings)
			if err != nil {
				return err
			}
			bars, issues := 0, 0
			for _, day := range dataset.Days {
				bars += len(day.Bars)
				if len(day.Issues) > 0 {
					issues++
				}
			}
			if _, err := fmt.Fprintf(out, "分足保存: %s / days=%d / bars=%d / quality_pending_days=%d\n実行記録: %s\n", path, len(dataset.Days), bars, issues, manifest); err != nil {
				return errors.New("変換結果を出力できません")
			}
			return nil
		}
		if json.Unmarshal(raw, &dataset) != nil {
			return errors.New("分足JSONを読み取れません")
		}
	}
	if command == "replay" {
		return replay(ctx, out, *input, raw, dataset, r001.Direction(*direction), r001.Variant(*variant))
	}
	if command == "compare-bars" {
		if *reference == "" {
			return errors.New("-referenceに照合先の分足JSONを指定してください")
		}
		refRaw, err := readInput(*reference)
		if err != nil {
			return err
		}
		var ref marketdata.Dataset
		if json.Unmarshal(refRaw, &ref) != nil {
			return errors.New("照合先の分足JSONを読み取れません")
		}
		report, err := comparebars.Compare(ctx, dataset, ref)
		if err != nil {
			return err
		}
		path, reportRaw, err := save("bar-comparison", report)
		if err != nil {
			return err
		}
		settings := struct{ Comparator, ReferenceFile, ReferenceSHA256 string }{comparebars.Version, filepath.Base(*reference), hash(refRaw)}
		manifest, err := recordRun(command, filepath.Base(*input), raw, dataset.Source.SHA256, path, reportRaw, settings)
		if err != nil {
			return err
		}
		for _, day := range report.Days {
			if _, err := fmt.Fprintf(out, "分足照合: date=%s / compared=%d / ohlcv_matches=%d / unfinished=%d / only_input=%d / only_reference=%d\n", day.Date, day.Compared, day.AllMatches, day.Unfinished, day.OnlyInput, day.OnlyReference); err != nil {
				return errors.New("照合結果を出力できません")
			}
		}
		_, err = fmt.Fprintf(out, "保存: %s\n実行記録: %s\nquality_promoted=false（同一提供元の照合・全約定や実約定の保証なし）\n", path, manifest)
		return err
	}
	inputName := filepath.Base(*input)
	if command == "demo" || (command == "replay" && *input == "") {
		var saved string
		if saved, raw, err = save("synthetic-bars", dataset); err != nil {
			return err
		}
		inputName = filepath.Base(saved)
	}
	report, err := backtest.RunVariant(ctx, dataset, r001.Direction(*direction), r001.Variant(*variant))
	if err != nil {
		return err
	}
	path, reportRaw, err := save("backtest", report)
	if err != nil {
		return err
	}
	settings := struct {
		Rule, Model string
		Direction   r001.Direction
		Variant     r001.Variant
		Costs       []int64
	}{report.Rule, report.Model, report.Direction, report.Variant, []int64{0, 5, 10, 20}}
	manifestPath, err := recordRun(command, inputName, raw, dataset.Source.SHA256, path, reportRaw, settings)
	if err != nil {
		return err
	}
	trades, blocked := 0, 0
	for _, day := range report.Days {
		if day.Trade != nil {
			trades++
		}
		if day.Status == "unevaluable" {
			blocked++
		}
	}
	if _, err = fmt.Fprintf(out, "研究結果: scope=%s / strategy=%s / days=%d / trades=%d / unevaluable=%d\n保存: %s\n実行記録: %s\n", report.Scope, report.Rule, len(report.Days), trades, blocked, path, manifestPath); err != nil {
		return errors.New("研究結果を出力できません")
	}
	return nil
}

func recordRun(command, inputName string, raw []byte, sourceHash, path string, reportRaw []byte, settings any) (string, error) {
	settingsRaw, _ := json.Marshal(settings)
	manifest := struct {
		Version        int             `json:"schema_version"`
		Created        time.Time       `json:"created_at"`
		Command        string          `json:"command"`
		InputFile      string          `json:"input_file"`
		InputSHA256    string          `json:"input_sha256"`
		SourceSHA256   string          `json:"source_sha256"`
		SettingsSHA256 string          `json:"settings_sha256"`
		Settings       json.RawMessage `json:"settings"`
		ReportSHA256   string          `json:"report_sha256"`
		ReportFile     string          `json:"report_file"`
		CodeRevision   string          `json:"code_revision"`
		WorkingTree    string          `json:"working_tree_at_build"`
		GoVersion      string          `json:"go_version"`
	}{Version: 1, Created: time.Now().UTC(), Command: command, InputFile: inputName, InputSHA256: hash(raw), SourceSHA256: sourceHash, SettingsSHA256: hash(settingsRaw), Settings: settingsRaw, ReportSHA256: hash(reportRaw), ReportFile: filepath.Base(path), CodeRevision: "unavailable", WorkingTree: "unknown"}
	if info, ok := debug.ReadBuildInfo(); ok {
		manifest.GoVersion = info.GoVersion
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				manifest.CodeRevision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				manifest.WorkingTree = setting.Value
			}
		}
	}
	manifestPath, _, err := save("research-run", manifest)
	return manifestPath, err
}
func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func readInput(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return nil, errors.New("入力は64MiB以内の通常ファイルを指定してください")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("入力ファイルを開けません")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(raw) > 64<<20 {
		return nil, errors.New("入力ファイルを読み取れません")
	}
	return raw, nil
}
func save(prefix string, value any) (string, []byte, error) {
	const directory = "data/research"
	raw, err := json.Marshal(value)
	if err != nil {
		return "", nil, errors.New("研究JSONを生成できません")
	}
	raw = append(raw, '\n')
	if os.MkdirAll(directory, 0700) != nil {
		return "", nil, errors.New("研究保存先を作成できません")
	}
	f, err := os.CreateTemp(directory, "."+prefix+"-*.tmp")
	if err != nil {
		return "", nil, errors.New("研究ファイルを作成できません")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return "", nil, errors.New("研究JSONを書き込めません")
	}
	if f.Sync() != nil {
		f.Close()
		return "", nil, errors.New("研究JSONを同期できません")
	}
	if f.Close() != nil {
		return "", nil, errors.New("研究JSONを閉じられません")
	}
	base := filepath.Base(tmp)
	final := filepath.Join(directory, base[1:len(base)-4]+".json")
	if os.Rename(tmp, final) != nil {
		return "", nil, errors.New("研究JSONを確定できません")
	}
	return final, raw, nil
}
