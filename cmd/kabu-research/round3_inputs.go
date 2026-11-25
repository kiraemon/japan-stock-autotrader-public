package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/jquantsbulk"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

const round3ScopeSHA = "05f28d1bf6511ce124183cfbc5ff0e03a522e4b9d6259bf5d7e70a21786ce49d"
const round3PeriodSHA = "20a3b7aeafa4114d265f253c7a6ef1064c71ea419ca4aafc59408b2d3f8b5f6c"
const round3Version = "jp-intraday-round3-study-v0.1"
const round3BookCount = 312

type round3Manifest struct {
	Version    int           `json:"version"`
	Scope      studyArtifact `json:"scope"`
	Period     studyArtifact `json:"period"`
	Universes  studyArtifact `json:"universe_index"`
	Normalized studyArtifact `json:"normalized_index"`
	Audit      studyArtifact `json:"independent_input_audit"`
}
type round3Partition struct {
	Sessions int      `json:"sessions"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Dates    []string `json:"dates"`
	Warmup   []string `json:"warmup_sessions"`
}
type round3MinuteFile struct {
	Partition  string `json:"partition"`
	Month      string `json:"month"`
	Symbol     string `json:"symbol"`
	Path       string `json:"path"`
	SHA        string `json:"sha256"`
	Capture    string `json:"capture"`
	CaptureSHA string `json:"capture_sha256"`
	SourceSHA  string `json:"source_sha256"`
	Days       int    `json:"days"`
	Bars       int    `json:"bars"`
	Events     int    `json:"boundary_events"`
	Excluded   int    `json:"excluded_source_rows"`
}
type round3Inputs struct {
	Manifest    round3Manifest
	Part        round3Partition
	Universe    universeOutput
	Sessions    []string
	FeeCalendar []string
	Daily       map[string]marketdata.DailyBar
	Files       map[string][]round3MinuteFile
}

func loadRound3Inputs(ctx context.Context, input string, raw []byte, phase string) (round3Inputs, error) {
	r := round3Inputs{Daily: map[string]marketdata.DailyBar{}, Files: map[string][]round3MinuteFile{}}
	fail := func() (round3Inputs, error) {
		return r, errors.New("第3回の固定計画/期間/母集団/分足索引の対応を確認してください")
	}
	m := &r.Manifest
	if strictJSON(raw, m) != nil || m.Version != 1 || m.Scope.SHA != round3ScopeSHA || m.Period.SHA != round3PeriodSHA {
		return fail()
	}
	pin := func(f studyArtifact) ([]byte, error) { return pinnedInput(relativeORBPath(input, f.Path), f.SHA) }
	if _, e := pin(m.Scope); e != nil {
		return r, e
	}
	periodRaw, e := pin(m.Period)
	if e != nil {
		return r, e
	}
	var period struct {
		Partitions map[string]round3Partition `json:"partitions"`
	}
	if json.Unmarshal(periodRaw, &period) != nil || len(period.Partitions) != 2 {
		return fail()
	}
	for _, name := range []string{"validation", "holdout"} {
		p := period.Partitions[name]
		count := 58
		from, to := "2024-12-02", "2025-02-28"
		if name == "holdout" {
			count = 40
			from = "2025-11-04"
			to = "2025-12-30"
		}
		if p.Sessions != count || len(p.Dates) != count || p.From != from || p.To != to || p.Dates[0] != from || p.Dates[count-1] != to {
			return fail()
		}
		r.FeeCalendar = append(r.FeeCalendar, p.Dates...)
	}
	r.Part = period.Partitions[phase]
	r.Sessions = append(append([]string{}, r.Part.Warmup...), r.Part.Dates...)
	uRaw, e := pin(m.Universes)
	if e != nil {
		return r, e
	}
	var ui struct {
		Version  int  `json:"version"`
		Complete bool `json:"complete"`
		PNL      bool `json:"candidate_pnl_evaluated"`
		Audit    bool `json:"independent_selection_audit_completed"`
		Records  []struct {
			Partition  string `json:"partition"`
			Path       string `json:"path"`
			SHA        string `json:"sha256"`
			Input      string `json:"input"`
			InputSHA   string `json:"input_sha256"`
			Days       int    `json:"days"`
			Codes      int    `json:"codes"`
			Catalog    string `json:"catalog"`
			CatalogSHA string `json:"catalog_sha256"`
		} `json:"records"`
	}
	if strictJSON(uRaw, &ui) != nil || ui.Version != 1 || !ui.Complete || ui.PNL || len(ui.Records) != 2 {
		return fail()
	}
	selected := ""
	phases := map[string]bool{}
	for _, entry := range ui.Records {
		if phases[entry.Partition] || entry.Partition != "validation" && entry.Partition != "holdout" {
			return fail()
		}
		phases[entry.Partition] = true
		if entry.Partition != phase {
			continue
		}
		selected = entry.Path
		body, e := pinnedInput(entry.Path, entry.SHA)
		if e != nil {
			return r, e
		}
		if strictJSON(body, &r.Universe) != nil || r.Universe.ManifestSHA != entry.InputSHA || r.Universe.Version != "s01-universe-v0.1" || r.Universe.Universe.Version != r.Universe.Version || r.Universe.Universe.QualityPromoted || r.Universe.Universe.TradingEligible || r.Universe.Universe.StrategyAdopted || r.Universe.Universe.NewPNL || len(r.Universe.Universe.Days) != r.Part.Sessions || r.Universe.DailySHA != entry.CatalogSHA || r.Universe.DailyCatalog != entry.Catalog {
			return fail()
		}
		manifestBody, e := pinnedInput(entry.Input, entry.InputSHA)
		if e != nil {
			return r, e
		}
		var original universeManifest
		if strictJSON(manifestBody, &original) != nil || original.Version != 2 || original.RuleSHA != r.Universe.RuleSHA {
			return fail()
		}
		if _, e := pinnedInput(relativeORBPath(entry.Input, original.RuleFile), original.RuleSHA); e != nil {
			return r, e
		}
		ruleSHA := "76e770cd43ad644cf122f853d533d6d76e3aa1c29d3a95ac1ca7095072a548f1"
		if phase == "holdout" {
			ruleSHA = "67ef012e08f245255c34e6927f0f5b8ff3ce2ba6d5df99cc02ea7b25d01dce5b"
		}
		if r.Universe.RuleSHA != ruleSHA {
			return fail()
		}
		for i, day := range r.Universe.Universe.Days {
			if day.Date != r.Part.Dates[i] || len(day.Picks) != 30 || len(day.Problems) > 0 {
				return fail()
			}
		}
		catalogRaw, e := pinnedInput(entry.Catalog, entry.CatalogSHA)
		if e != nil {
			return r, e
		}
		var catalog universeDailyCatalog
		if strictJSON(catalogRaw, &catalog) != nil || catalog.Version != "jquants-unadjusted-daily-v0.1" || catalog.QualityPromoted || catalog.Origin != "observed" || catalog.PriceScale != marketdata.PriceScale || !reflect.DeepEqual(catalog.Sources, r.Universe.Sources) {
			return fail()
		}
		for _, d := range catalog.Bars {
			key := d.Code + "/" + d.Date
			if _, exists := r.Daily[key]; exists {
				return fail()
			}
			r.Daily[key] = d
		}
	}
	if selected == "" {
		return fail()
	}
	calendar := []string{}
	sources := append([]universeSource{}, r.Universe.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Month < sources[j].Month })
	for _, source := range sources {
		if source.Kind != "calendar" {
			if source.Kind != "daily" && source.Kind != "master" {
				return fail()
			}
			actual, e := universeCSV(ctx, source.Capture, source.Kind, func(reader io.Reader, _ string, _ time.Time) (int, error) {
				n, e := io.Copy(io.Discard, io.LimitReader(reader, (256<<20)+1))
				if e != nil || n > 256<<20 {
					return 0, errors.New("元の日次gzipの圧縮整合性/容量を確認してください")
				}
				return source.Rows, nil
			})
			if e != nil {
				return r, e
			}
			if !reflect.DeepEqual(actual, source) {
				return fail()
			}
			continue
		}
		body, e := pinnedInput(source.Capture, source.CaptureSHA)
		if e != nil {
			return r, e
		}
		c, actual, e := readUniverseCapture(source.Capture, "calendar")
		if e != nil || hash(body) != source.CaptureSHA || !reflect.DeepEqual(actual, source) {
			return fail()
		}
		days, e := marketdata.JQuantsSessions(c.Calendar, c.Month)
		if e != nil {
			return r, e
		}
		calendar = append(calendar, days...)
	}
	if !reflect.DeepEqual(calendar, r.Sessions) {
		return fail()
	}
	nRaw, e := pin(m.Normalized)
	if e != nil {
		return r, e
	}
	var ni struct {
		Version     int                `json:"version"`
		Universe    string             `json:"universe_index"`
		UniverseSHA string             `json:"universe_index_sha256"`
		Records     []round3MinuteFile `json:"records"`
		Complete    bool               `json:"complete"`
		PNL         bool               `json:"candidate_pnl_evaluated"`
		Audit       bool               `json:"independent_reconciliation_completed"`
		Quality     bool               `json:"quality_promoted"`
	}
	if strictJSON(nRaw, &ni) != nil || ni.Version != 1 || !ni.Complete || ni.PNL || ni.Quality || ni.UniverseSHA != m.Universes.SHA || len(ni.Records) != 292 {
		return fail()
	}
	seen := map[string]bool{}
	for _, f := range ni.Records {
		if f.Partition != "validation" && f.Partition != "holdout" {
			return fail()
		}
		key := f.Partition + "/" + f.Symbol + "/" + f.Month
		if seen[key] {
			return fail()
		}
		seen[key] = true
		if f.Partition != phase {
			continue
		}
		codes := r.Universe.Universe.MonthlyCodes[f.Month]
		code := strings.TrimSuffix(f.Symbol, "@1") + "0"
		at := sort.SearchStrings(codes, code)
		if at >= len(codes) || codes[at] != code {
			return fail()
		}
		r.Files[f.Symbol] = append(r.Files[f.Symbol], f)
	}
	count := 0
	for _, codes := range r.Universe.Universe.MonthlyCodes {
		count += len(codes)
	}
	found := 0
	for _, files := range r.Files {
		found += len(files)
	}
	if found != count || len(r.Files) != len(r.Universe.Universe.Union) {
		return fail()
	}
	auditRaw, e := pin(m.Audit)
	if e != nil {
		return r, e
	}
	var audit struct {
		IndexSHA           string            `json:"bars_index_sha256"`
		PNL                bool              `json:"candidate_pnl_evaluated"`
		Quality            bool              `json:"quality_promoted"`
		Files              int               `json:"normalized_files"`
		DailyOK            bool              `json:"all_daily_ohlcv_and_turnover_match"`
		RawOK              bool              `json:"raw_csv_to_normalized_fields_match"`
		ProductionImported bool              `json:"production_packages_imported"`
		Issues             []json.RawMessage `json:"daily_ohlcv_turnover_issues"`
	}
	if json.Unmarshal(auditRaw, &audit) != nil || audit.IndexSHA != m.Normalized.SHA || audit.PNL || audit.Quality || audit.Files != 292 || !audit.DailyOK || !audit.RawOK || audit.ProductionImported || len(audit.Issues) > 0 {
		return fail()
	}
	if ctx.Err() != nil {
		return r, errors.New("第3回の入力検査を中断しました")
	}
	return r, nil
}

func round3Joined(ctx context.Context, files []round3MinuteFile, checked map[string]string) (marketdata.Dataset, error) {
	monthly := []marketdata.Dataset{}
	sort.Slice(files, func(i, j int) bool { return files[i].Month < files[j].Month })
	for _, f := range files {
		if ctx.Err() != nil {
			return marketdata.Dataset{}, errors.New("第3回の分足読込を中断しました")
		}
		key := f.Capture + "/" + f.CaptureSHA
		identity := f.Month + "/" + f.SourceSHA
		if prior, exists := checked[key]; exists && prior != identity {
			return marketdata.Dataset{}, errors.New("元分足の月/hash参照が矛盾しています")
		}
		if checked[key] == "" {
			raw, e := pinnedInput(f.Capture, f.CaptureSHA)
			if e != nil {
				return marketdata.Dataset{}, e
			}
			var c jquantsbulk.Report
			if strictJSON(raw, &c) != nil || !c.Complete || c.ListOnly || c.CalendarOnly || c.Month != f.Month || len(c.Files) != 1 || c.Dataset != "" && c.Dataset != jquantsbulk.Minute || c.Files[0].SHA256 != f.SourceSHA || filepath.Base(c.Files[0].Local) != c.Files[0].Local || strings.ContainsAny(c.Files[0].Local, "/\\") || c.Files[0].Size <= 0 || c.Files[0].Size > 512<<20 {
				return marketdata.Dataset{}, errors.New("固定した元分足captureを確認してください")
			}
			file, e := os.Open(filepath.Join(filepath.Dir(f.Capture), c.Files[0].Local))
			if e != nil {
				return marketdata.Dataset{}, errors.New("元分足gzipを確認できません")
			}
			h := sha256.New()
			n, e := io.Copy(h, io.LimitReader(file, c.Files[0].Size+1))
			closeErr := file.Close()
			if e != nil || closeErr != nil || n != c.Files[0].Size || hex.EncodeToString(h.Sum(nil)) != f.SourceSHA {
				return marketdata.Dataset{}, errors.New("元分足gzipのhash/容量が一致しません")
			}
			checked[key] = identity
		}
		body, e := pinnedInput(f.Path, f.SHA)
		if e != nil {
			return marketdata.Dataset{}, e
		}
		var d marketdata.Dataset
		if strictJSON(body, &d) != nil || d.Symbol != f.Symbol || d.Source.SHA256 != f.SourceSHA || d.Source.Format != marketdata.JQuantsTurnoverFormat || len(d.Days) != f.Days {
			return marketdata.Dataset{}, errors.New("固定した売買代金付き月次分足を確認してください")
		}
		bars, events, excluded := 0, 0, 0
		for _, day := range d.Days {
			if !strings.HasPrefix(day.Date, f.Month+"-") {
				return marketdata.Dataset{}, errors.New("月次分足の日付が一致しません")
			}
			bars += len(day.Bars)
			events += len(day.Events)
		}
		for _, x := range d.ExcludedRows {
			excluded += x.Rows
		}
		if bars != f.Bars || events != f.Events || excluded != f.Excluded {
			return marketdata.Dataset{}, errors.New("月次分足の件数が一致しません")
		}
		monthly = append(monthly, d)
	}
	return buildbars.JoinJQuantsWithTurnover(ctx, monthly)
}
