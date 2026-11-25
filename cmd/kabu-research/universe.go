package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/prepareuniverse"
	"github.com/kiraemon/japan-stock-trading-lab/internal/jquantsbulk"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

type universeManifest struct {
	Version       int      `json:"version"`
	RuleFile      string   `json:"rule_file"`
	RuleSHA       string   `json:"rule_sha256"`
	DailyFiles    []string `json:"daily_files"`
	MasterFiles   []string `json:"master_files"`
	CalendarFiles []string `json:"calendar_files"`
}
type universeSource struct {
	Kind       string    `json:"dataset"`
	Month      string    `json:"month"`
	Capture    string    `json:"capture"`
	CaptureSHA string    `json:"capture_sha256"`
	GzipSHA    string    `json:"gzip_sha256,omitempty"`
	Rows       int       `json:"rows,omitempty"`
	Captured   time.Time `json:"captured_at"`
}
type universeOutput struct {
	Version      string                 `json:"version"`
	ManifestSHA  string                 `json:"manifest_sha256"`
	RuleSHA      string                 `json:"rule_sha256"`
	Sources      []universeSource       `json:"sources"`
	Universe     prepareuniverse.Report `json:"universe"`
	DailyCatalog string                 `json:"daily_catalog_file"`
	DailySHA     string                 `json:"daily_catalog_sha256"`
}
type universeDailyCatalog struct {
	Version         string                `json:"version"`
	Origin          string                `json:"origin"`
	PriceScale      int64                 `json:"price_scale"`
	Sources         []universeSource      `json:"sources"`
	Bars            []marketdata.DailyBar `json:"bars"`
	QualityPromoted bool                  `json:"quality_promoted"`
}

func strictJSON(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("研究入力JSONの形式を確認してください")
	}
	return nil
}

func readUniverseCapture(path string, kind string) (jquantsbulk.Report, universeSource, error) {
	var r jquantsbulk.Report
	source := universeSource{Kind: kind, Capture: path}
	raw, err := readInput(path)
	if err != nil {
		return r, source, err
	}
	if strictJSON(raw, &r) != nil || r.Version != jquantsbulk.Version || !r.Complete || r.ListOnly || r.Started.IsZero() || r.Captured.Before(r.Started) || r.LastStatus != 200 || r.Requests < 1 || jquantsbulk.ValidateMonth(r.Month) != nil {
		return r, source, errors.New("完了した公式一括取得記録を確認してください")
	}
	if kind == "calendar" {
		if !r.CalendarOnly || len(r.Files) != 0 || (r.Dataset != "" && r.Dataset != jquantsbulk.Minute) {
			return r, source, errors.New("公式カレンダー取得記録を確認してください")
		}
	} else if r.CalendarOnly || string(r.Dataset) != kind || len(r.Files) != 1 {
		return r, source, errors.New("日足/銘柄一覧の取得種類を確認してください")
	}
	source.Month, source.CaptureSHA, source.Captured = r.Month, hash(raw), r.Captured
	return r, source, nil
}

func universeCSV(ctx context.Context, path, kind string, consume func(io.Reader, string, time.Time) (int, error)) (universeSource, error) {
	r, source, err := readUniverseCapture(path, kind)
	if err != nil {
		return source, err
	}
	f := r.Files[0]
	if f.Local == "" || filepath.Base(f.Local) != f.Local || strings.ContainsAny(f.Local, "/\\") || !strings.HasSuffix(f.Local, ".csv.gz") || f.Size <= 0 || f.Size > 512<<20 || !strings.HasPrefix(f.Key, map[string]string{"daily": "equities/bars/daily/", "master": "equities/master/"}[kind]) {
		return source, errors.New("日次原gzipの保存先/容量/種類を確認してください")
	}
	file, err := os.Open(filepath.Join(filepath.Dir(path), f.Local))
	if err != nil {
		return source, errors.New("日次原gzipを読み込めません")
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, f.Size+1))
	if err != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return source, errors.New("日次原gzipのhash/容量が取得記録と一致しません")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return source, errors.New("日次原gzipを再読込できません")
	}
	z, err := gzip.NewReader(file)
	if err != nil {
		return source, errors.New("日次gzip形式を確認してください")
	}
	defer z.Close()
	if ctx.Err() != nil {
		return source, errors.New("日次入力を中断しました")
	}
	count, err := consume(z, r.Month, r.Captured)
	if err != nil {
		return source, err
	}
	source.GzipSHA, source.Rows = f.SHA256, count
	return source, nil
}

func universeRule(raw []byte) (string, string, error) {
	// Descriptive fields stay in the original, hashed JSON. Arithmetic fields
	// must match the one implemented research rule; this is not a tuning API.
	var r struct {
		Version  int      `json:"version"`
		ID       string   `json:"rule_id"`
		From     string   `json:"evaluation_from"`
		To       string   `json:"evaluation_to"`
		Product  string   `json:"product_category"`
		Markets  []string `json:"markets"`
		Lookback int      `json:"liquidity_lookback_sessions"`
		Size     int      `json:"universe_size"`
		Tie      string   `json:"liquidity_tie_break"`
		Orders   bool     `json:"real_orders_enabled"`
		Adopted  bool     `json:"strategy_adopted"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Version != 1 || r.ID != "s01-universe-v1" || r.Product != "011" || strings.Join(r.Markets, ",") != "0111,0112,0113" || r.Lookback != prepareuniverse.Lookback || r.Size != prepareuniverse.Size || r.Tie != "Code ascending" || r.Orders || r.Adopted {
		return "", "", errors.New("固定したS01母集団の条件を確認してください")
	}
	return r.From, r.To, nil
}

// A report can reference the same immutable catalog on repeated runs without
// putting a random output filename into its reproducible content.
func saveUniverseCatalog(value universeDailyCatalog) (string, []byte, error) {
	return saveContentAddressed("daily-catalog", value)
}

func runUniverse(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("select-universe", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "固定条件・日足/当時の一覧・公式カレンダー取得記録のmanifest（オフライン専用）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *input == "" {
		return errors.New("select-universeにはinputのmanifestだけを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	var m universeManifest
	if strictJSON(raw, &m) != nil || (m.Version != 1 && m.Version != 2) || m.RuleFile == "" || len(m.RuleSHA) != 64 || len(m.DailyFiles) == 0 || len(m.DailyFiles) > 24 || len(m.MasterFiles) != len(m.DailyFiles) || len(m.CalendarFiles) != len(m.DailyFiles) {
		return errors.New("母集団manifestの版/条件/入力件数を確認してください")
	}
	resolve := func(path string) string { return relativeORBPath(*input, path) }
	ruleRaw, err := readInput(resolve(m.RuleFile))
	if err != nil {
		return err
	}
	if hash(ruleRaw) != m.RuleSHA {
		return errors.New("固定した母集団条件のhashが一致しません")
	}
	from, to, err := universeRule(ruleRaw)
	if err != nil {
		return err
	}
	r := universeOutput{Version: prepareuniverse.Version, ManifestSHA: hash(raw), RuleSHA: hash(ruleRaw), Sources: []universeSource{}}
	calendarMonths := map[string][]string{}
	for _, path := range m.CalendarFiles {
		capture, p, err := readUniverseCapture(resolve(path), "calendar")
		if err != nil {
			return err
		}
		if _, exists := calendarMonths[capture.Month]; exists {
			return errors.New("公式カレンダー月が重複しています")
		}
		sessions, err := marketdata.JQuantsSessions(capture.Calendar, capture.Month)
		if err != nil {
			return err
		}
		calendarMonths[capture.Month] = sessions
		r.Sources = append(r.Sources, p)
	}
	months := []string{}
	for month := range calendarMonths {
		months = append(months, month)
	}
	sort.Strings(months)
	sessions := []string{}
	for i, month := range months {
		if i > 0 {
			at, _ := time.Parse("2006-01", months[i-1])
			if at.AddDate(0, 1, 0).Format("2006-01") != month {
				return errors.New("公式カレンダーの月が連続していません")
			}
		}
		sessions = append(sessions, calendarMonths[month]...)
	}
	daily := []marketdata.DailyBar{}
	listings := []prepareuniverse.Listing{}
	for _, group := range []struct {
		kind  string
		paths []string
	}{{"daily", m.DailyFiles}, {"master", m.MasterFiles}} {
		seen := map[string]bool{}
		for _, path := range group.paths {
			p, err := universeCSV(ctx, resolve(path), group.kind, func(reader io.Reader, month string, captured time.Time) (int, error) {
				if group.kind == "daily" {
					read := prepareuniverse.ReadDaily
					if m.Version == 2 {
						read = prepareuniverse.ReadDailyHistory
					}
					rows, err := read(ctx, reader, month)
					if err != nil {
						return 0, err
					}
					for _, d := range rows {
						if d.Date > marketdata.Date(captured) {
							return 0, errors.New("取得時刻より後の日足が含まれています")
						}
					}
					daily = append(daily, rows...)
					return len(rows), nil
				}
				read := prepareuniverse.ReadListings
				if m.Version == 2 {
					read = prepareuniverse.ReadListingsHistory
				}
				rows, err := read(ctx, reader, month)
				if err != nil {
					return 0, err
				}
				listings = append(listings, rows...)
				return len(rows), nil
			})
			if err != nil {
				return err
			}
			if seen[p.Month] || calendarMonths[p.Month] == nil {
				return errors.New("日次入力の重複月/カレンダー不足を確認してください")
			}
			seen[p.Month] = true
			r.Sources = append(r.Sources, p)
		}
		for _, month := range months {
			if !seen[month] {
				return errors.New("営業月に対応する日次入力が不足しています")
			}
		}
	}
	r.Universe, err = prepareuniverse.Select(ctx, daily, listings, sessions, from, to)
	if err != nil {
		return err
	}
	union := map[string]bool{}
	for _, code := range r.Universe.Union {
		union[code] = true
	}
	catalog := universeDailyCatalog{Version: "jquants-unadjusted-daily-v0.1", Origin: "observed", PriceScale: marketdata.PriceScale, Sources: r.Sources, Bars: []marketdata.DailyBar{}}
	for _, bar := range daily {
		if union[bar.Code] {
			catalog.Bars = append(catalog.Bars, bar)
		}
	}
	sort.Slice(catalog.Bars, func(i, j int) bool { a, b := catalog.Bars[i], catalog.Bars[j]; return a.Date+a.Code < b.Date+b.Code })
	if ctx.Err() != nil {
		return errors.New("母集団の保存を中断しました")
	}
	catalogPath, rawCatalog, err := saveUniverseCatalog(catalog)
	if err != nil {
		return err
	}
	r.DailyCatalog = catalogPath
	r.DailySHA = hash(rawCatalog)
	path, body, err := save("universe", r)
	if err != nil {
		return err
	}
	run, err := recordRun("select-universe", filepath.Base(*input), raw, hash(raw), path, body, m)
	if err != nil {
		return err
	}
	excluded, problemDays := 0, 0
	for _, day := range r.Universe.Days {
		excluded += len(day.Missing)
		if len(day.Problems) > 0 {
			problemDays++
		}
	}
	_, err = fmt.Fprintf(out, "母集団: days=%d / unique_codes=%d / excluded_missing_stock_days=%d / problem_days=%d / quality_promoted=false / new_pnl_evaluation=false\n保存: %s\n日足保存: %s\n実行記録: %s\n", len(r.Universe.Days), len(r.Universe.Union), excluded, problemDays, path, r.DailyCatalog, run)
	return err
}
