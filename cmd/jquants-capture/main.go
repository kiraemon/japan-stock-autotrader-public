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
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/credentials"
	"github.com/kiraemon/japan-stock-autotrader/internal/instanceguard"
	"github.com/kiraemon/japan-stock-autotrader/internal/jquantscli"
	"github.com/kiraemon/japan-stock-autotrader/internal/jquantsrate"
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
	fs := flag.NewFlagSet("jquants-capture", flag.ContinueOnError)
	fs.SetOutput(out)
	root := fs.String("project-root", ".", "プロジェクトのルート（J-Quants分足の読み取り専用）")
	codes := fs.String("codes", "7203,8306,6501", "1〜3銘柄の4桁コード")
	from := fs.String("from", "", "取得開始日YYYY-MM-DD（必須）")
	to := fs.String("to", "", "取得終了日YYYY-MM-DD（必須、31日以内）")
	bulkMonth := fs.String("bulk-month", "", "YYYY-MMの分足一括gzip取得（from/to/codesとの併用不可）")
	listOnly := fs.Bool("list-only", false, "bulk-monthの一覧と容量だけ確認")
	calendarOnly := fs.Bool("calendar-only", false, "bulk-monthの公式取引カレンダーだけ取得")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("不要な引数があります")
	}
	if *bulkMonth != "" {
		conflict := *from != "" || *to != ""
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "codes" {
				conflict = true
			}
		})
		if conflict || (*calendarOnly && *listOnly) {
			return errors.New("bulk-monthはfrom/to/codesと併用できません")
		}
		return runBulk(ctx, *root, *bulkMonth, *listOnly, *calendarOnly, out)
	}
	if *listOnly || *calendarOnly {
		return errors.New("list-onlyはbulk-monthにだけ指定できます")
	}
	jobs := []jquantscli.Job{}
	for _, code := range strings.Split(*codes, ",") {
		jobs = append(jobs, jquantscli.Job{Code: code, From: *from, To: *to})
	}
	if err := jquantscli.ValidateJobs(jobs); err != nil {
		return err
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return errors.New("プロジェクトのパスを確認してください")
	}
	executable := filepath.Join(absolute, ".tools", "jquants", "jquants.exe")
	raw, err := os.ReadFile(executable)
	if err != nil {
		return errors.New("公式J-Quants CLIをプロジェクト内へ導入してください")
	}
	digest := sha256.Sum256(raw)
	clear(raw)
	if hex.EncodeToString(digest[:]) != jquantscli.EXESHA256 {
		return errors.New("公式CLIの固定版ハッシュが一致しません")
	}
	gate, err := instanceguard.Acquire("Global\\JapanStockAutotrader.JQuantsCapture.v1")
	if err != nil {
		return errors.New("J-Quantsの取得処理が起動中、または取得ロックを利用できません")
	}
	defer gate.Close()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	lease, err := jquantsrate.Start(ctx, filepath.Join(absolute, ".cache", "jquants-rate.json"), 3*time.Minute)
	if err != nil {
		return err
	}
	rateLimited := false
	defer func() {
		if lease.Finish(rateLimited) != nil {
			fmt.Fprintln(out, "送信間隔記録の更新失敗。次回起動は予約時刻まで停止します")
		}
	}()
	key, err := credentials.JQuants(ctx, absolute)
	if err != nil {
		return err
	}
	results, stats, captureErr := jquantscli.Capture(ctx, executable, key, jobs)
	rateLimited = stats.LastStatus == 429
	key = ""
	directory := filepath.Join(absolute, "data", "jquants", "captures")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("取得データの保存先を作成できません")
	}
	batch := struct {
		Version  string              `json:"version"`
		Provider string              `json:"provider"`
		CLI      string              `json:"cli_version"`
		Stats    jquantscli.Stats    `json:"request_stats"`
		Complete bool                `json:"complete"`
		Results  []jquantscli.Result `json:"results"`
	}{jquantscli.Version, "jquants", jquantscli.CLIVersion, stats, captureErr == nil, results}
	body, err := json.Marshal(batch)
	if err != nil {
		return errors.New("取得データをJSONへ保存できません")
	}
	f, err := os.CreateTemp(directory, ".capture-*.tmp")
	if err != nil {
		return errors.New("取得データの保存を開始できません")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, writeErr := f.Write(body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("取得データの保存に失敗しました")
	}
	base := filepath.Base(tmp)
	final := filepath.Join(directory, base[1:len(base)-4]+".json")
	if os.Rename(tmp, final) != nil {
		return errors.New("取得ファイルを確定できません")
	}
	_, err = fmt.Fprintf(out, "J-Quants取得: complete=%t / symbols=%d / api_requests=%d / last_status=%d / min_interval_ms=%d\n保存: %s\n", captureErr == nil, len(results), stats.Requests, stats.LastStatus, stats.IntervalMS, final)
	if captureErr != nil {
		return captureErr
	}
	return err
}
