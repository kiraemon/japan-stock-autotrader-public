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
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func readFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, fail("Local analysis input unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if e != nil || len(b) > 64<<20 {
		return nil, fail("Local analysis input exceeds size or read bound")
	}
	return b, nil
}
func readPin(p pin, v any) error {
	if !shaPattern.MatchString(p.SHA) {
		return fail("Source SHA256 missing")
	}
	b, e := readFile(p.Path)
	if e != nil {
		return e
	}
	if digest(b) != p.SHA {
		return fail("Pinned analysis input hash mismatch")
	}
	return decode(b, v)
}
func primaryBooks(s study) ([]bookRef, error) {
	selected := map[string]bookRef{}
	for _, r := range s.Books {
		c := r.Summary.Scenario
		if r.Cohort != "native" || c.Common || c.BPS != 10 || c.Commission != "sor_conditional_zero" || c.Extra != 0 {
			continue
		}
		if r.Phase != s.Phase {
			return nil, fail("Primary book partition mismatch")
		}
		if _, dup := selected[r.Model]; dup {
			return nil, fail("Duplicate primary model")
		}
		selected[r.Model] = r
	}
	result := []bookRef{}
	for _, m := range models {
		r, ok := selected[m]
		if !ok {
			return nil, fail("Primary fixed model missing")
		}
		result = append(result, r)
	}
	if len(selected) != 3 {
		return nil, fail("Unexpected primary model")
	}
	return result, nil
}
func loadStudy(p pin, m manifest, indexSHA string) (study, []bookRef, error) {
	var s study
	if e := readPin(p, &s); e != nil {
		return s, nil, e
	}
	n := 58
	from, to := "2024-12-02", "2025-02-28"
	if s.Phase == "holdout" {
		n = 40
		from, to = "2025-11-04", "2025-12-30"
	} else if s.Phase != "validation" {
		return s, nil, fail("Analysis is restricted to opened round-three partitions")
	}
	if s.Version != "jp-intraday-round3-study-v0.1" || s.ManifestSHA != m.Input.SHA || s.ScopeSHA != scopeSHA || s.IndexSHA != indexSHA || len(s.Dates) != n || len(s.Books) != 312 || s.Dates[0] != from || s.Dates[n-1] != to || s.Adopted || s.Trading {
		return s, nil, fail("Opened study contract mismatch")
	}
	for i, date := range s.Dates {
		d, e := time.Parse("2006-01-02", date)
		if e != nil || d.Format("2006-01-02") != date || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday || i > 0 && s.Dates[i-1] >= date {
			return s, nil, fail("Official study sessions invalid")
		}
	}
	refs, e := primaryBooks(s)
	return s, refs, e
}

func prepareAnalysis(s study, root pin, r bookRef) (analysis, book, error) {
	a := analysis{Phase: s.Phase, Model: r.Model, Root: root, Book: r.Artifact, Sessions: len(s.Dates), Groups: map[string]group{}, Paths: map[string]pathSummary{}, Daily: map[string]string{}, Intervals: []interval{}}
	var b book
	if e := readPin(r.Artifact, &b); e != nil {
		return a, b, e
	}
	if b.Version != "ordinary-margin-research-book-v0.1" || b.Scenario != r.Summary.Scenario || b.Candidates != len(b.Ledger) || b.Cases != len(s.Dates)*30 || b.Unevaluable != r.Summary.Unevaluable || b.Executed != r.Summary.Executed || b.Net != r.Summary.Net || b.After != r.Summary.After || b.Adopted || b.Trading {
		return a, b, fail("Primary ledger or summary mismatch")
	}
	a.Cases = b.Cases
	a.Unevaluable = b.Unevaluable
	a.Executed = b.Executed
	a.AfterData = b.After
	byDate := map[string]*big.Rat{}
	grossSum, netSum := new(big.Rat), new(big.Rat)
	active := map[string]bool{}
	for _, date := range s.Dates {
		byDate[date] = new(big.Rat)
	}
	count := 0
	seen := map[string]bool{}
	for _, e := range b.Ledger {
		key := e.Date + "/" + e.Symbol
		if seen[key] || byDate[e.Date] == nil || e.Rank < 1 || e.Rank > 30 || e.Trade.Entry <= 0 || e.Trade.Exit <= 0 || e.Trade.Entry > 1000000000000 || e.Trade.Exit > 1000000000000 || e.Trade.EntryAt.In(jst).Format("2006-01-02") != e.Date || e.Status != "executed" && e.Status != "skipped_busy" && e.Status != "skipped_capital" {
			return a, b, fail("Frozen candidate dimensions invalid")
		}
		seen[key] = true
		sign := int64(1)
		if e.Trade.Direction == "short" {
			sign = -1
		} else if e.Trade.Direction != "long" {
			return a, b, fail("Frozen direction invalid")
		}
		g, er := exact(e.Costs.Gross)
		if er != nil {
			return a, b, er
		}
		net, er := exact(e.Costs.Net)
		if er != nil {
			return a, b, er
		}
		expected := big.NewRat(sign*(e.Trade.Exit-e.Trade.Entry), 100)
		if g.Cmp(expected) != 0 {
			return a, b, fail("Frozen gross and price correspondence mismatch")
		}
		for _, key := range groupKeys(e) {
			v := a.Groups[key]
			if er := v.add(e); er != nil {
				return a, b, er
			}
			a.Groups[key] = v
		}
		if e.Status == "executed" {
			count++
			active[e.Date] = true
			byDate[e.Date].Add(byDate[e.Date], net)
			grossSum.Add(grossSum, g)
			netSum.Add(netSum, net)
		}
	}
	gross, e := exact(b.Gross)
	if e != nil {
		return a, b, e
	}
	net, e := exact(b.Net)
	if e != nil {
		return a, b, e
	}
	data, e := exact(b.Data)
	if e != nil {
		return a, b, e
	}
	after, e := exact(b.After)
	if e != nil {
		return a, b, e
	}
	if count != b.Executed || gross.Cmp(grossSum) != 0 || net.Cmp(netSum) != 0 || new(big.Rat).Sub(net, data).Cmp(after) != 0 {
		return a, b, fail("Executed cash flows do not reconcile")
	}
	a.ActiveDays = len(active)
	a.fee, _ = data.Float64()
	for _, date := range s.Dates {
		v := byDate[date]
		a.Daily[date] = v.RatString()
		f, _ := v.Float64()
		a.series = append(a.series, f)
	}
	return a, b, nil
}

func loadDays(ctx context.Context, phase, symbol string, months map[string]bool, files []minuteFile) (map[string]day, []pin, error) {
	days := map[string]day{}
	pins := []pin{}
	for _, f := range files {
		if ctx.Err() != nil {
			return nil, nil, fail("Path analysis interrupted")
		}
		if f.Phase != phase || f.Symbol != symbol || !months[f.Month] {
			continue
		}
		var d dataset
		p := pin{f.Path, f.SHA}
		if e := readPin(p, &d); e != nil {
			return nil, nil, e
		}
		if d.Version != 1 || d.Scale != 10000 || d.Symbol != symbol || d.Origin != "observed" || d.Environment != "production" || d.Source.Format != "jquants-bulk-minute-turnover-v0.1" || d.Source.Label != "start" || d.Source.SHA != f.SourceSHA {
			return nil, nil, fail("Normalized observed source mismatch")
		}
		pins = append(pins, p)
		for _, v := range d.Days {
			if _, exists := days[v.Date]; exists || !strings.HasPrefix(v.Date, f.Month+"-") {
				return nil, nil, fail("Normalized source day duplicated or out of month")
			}
			prior := time.Time{}
			for _, b := range v.Bars {
				if b.At.In(jst).Format("2006-01-02") != v.Date || !b.At.Equal(b.At.Truncate(time.Minute)) || !prior.IsZero() && !b.At.After(prior) || b.Low <= 0 || b.High > 1000000000000 || b.High < b.Low || b.Open < b.Low || b.Open > b.High || b.Close < b.Low || b.Close > b.High {
					return nil, nil, fail("Normalized path bar invalid")
				}
				prior = b.At
			}
			days[v.Date] = v
		}
	}
	if len(pins) == 0 {
		return nil, nil, fail("No matching normalized source")
	}
	return days, pins, nil
}

func analyze(ctx context.Context, m manifest, manifestSHA string) (output, error) {
	out := output{Version: version, ManifestSHA: manifestSHA, Plan: m.Plan, Studies: m.Studies, Files: []pin{}, Results: []analysis{}, Paired: []paired{}, Limitations: []string{"exploratory_on_opened_data_no_new_final_or_strategy_or_adoption", "fixed_realized_trade_cashflows_not_counterfactual_capital_path_replay", "circular_blocks_assume_observed_short_run_dependence_is_informative_not_proven_stationarity", "intervals_conditional_not_probability_of_true_profitability_not_multiplicity_adjusted", "zero_book_cashflow_days_do_not_reclassify_unevaluable_source_cases", "fixed_horizon_MFE_MAE_are_observed_extremes_not_attainable_fills_and_include_time_after_S01_stop", "intrabar_stop_bar_extremes_excluded_from_post_exit_recovery", "candidate_group_means_are_not_simultaneously_executable_portfolios_or_causal_selection_effects", "same_provider_normalized_source_pins_rechecked_not_full_original_gzip_reconversion", "original_rules_prices_fees_quality_flags_freeze_and_decisions_preserved"}}
	if m.Version != 1 || len(m.Studies) != 2 {
		return out, fail("Analysis manifest invalid")
	}
	var p plan
	if e := readPin(m.Plan, &p); e != nil {
		return out, e
	}
	if p.Version != 1 || p.ID != version || !reflect.DeepEqual(p.Models, models) || p.Replicates != 20000 || p.Seed != 20261019 || !reflect.DeepEqual(p.Blocks, []int{1, 5}) || p.Primary != 5 || p.Orders {
		return out, fail("Fixed exploratory analysis plan mismatch")
	}
	out.Replicates = p.Replicates
	out.Seed = p.Seed
	var im inputManifest
	if e := readPin(m.Input, &im); e != nil {
		return out, e
	}
	if im.Scope.SHA != scopeSHA {
		return out, fail("Original strategy plan differs")
	}
	var scope any
	if e := readPin(im.Scope, &scope); e != nil {
		return out, e
	}
	var idx normalizedIndex
	if e := readPin(im.Index, &idx); e != nil {
		return out, e
	}
	if !idx.Complete || idx.PNL || idx.Quality || len(idx.Records) != 292 {
		return out, fail("Original normalized index mismatch")
	}
	seenPhases := map[string]bool{}
	for _, root := range m.Studies {
		s, refs, e := loadStudy(root, m, im.Index.SHA)
		if e != nil {
			return out, e
		}
		if seenPhases[s.Phase] {
			return out, fail("Analysis partition duplicated")
		}
		seenPhases[s.Phase] = true
		start := len(out.Results)
		books := []book{}
		bySymbol := map[string][]struct {
			model int
			entry entry
		}{}
		for i, r := range refs {
			a, b, e := prepareAnalysis(s, root, r)
			if e != nil {
				return out, e
			}
			out.Results = append(out.Results, a)
			books = append(books, b)
			for _, v := range b.Ledger {
				bySymbol[v.Symbol] = append(bySymbol[v.Symbol], struct {
					model int
					entry entry
				}{i, v})
			}
		}
		symbols := []string{}
		for symbol := range bySymbol {
			symbols = append(symbols, symbol)
		}
		sort.Strings(symbols)
		months := map[string]bool{}
		for _, date := range s.Dates {
			months[date[:7]] = true
		}
		for _, symbol := range symbols {
			days, pins, e := loadDays(ctx, s.Phase, symbol, months, idx.Records)
			if e != nil {
				return out, e
			}
			out.Files = append(out.Files, pins...)
			for _, v := range bySymbol[symbol] {
				x, e := inspectPath(v.entry, days[v.entry.Date].Bars, models[v.model])
				if e != nil {
					return out, e
				}
				a := &out.Results[start+v.model]
				for _, key := range []string{"all", "status/" + v.entry.Status} {
					ps := a.Paths[key]
					ps.add(x)
					a.Paths[key] = ps
				}
			}
		}
		series := [][]float64{}
		fees := []float64{}
		for i := 0; i < 3; i++ {
			a := &out.Results[start+i]
			if a.Paths["all"].Count != books[i].Candidates {
				return out, fail("Path candidate count mismatch")
			}
			series = append(series, a.series)
			fees = append(fees, a.fee)
		}
		pairs := [][2]int{{0, 1}, {0, 2}, {1, 2}}
		pairReports := make([]paired, 3)
		for i, pair := range pairs {
			a, b := out.Results[start+pair[0]], out.Results[start+pair[1]]
			av, _ := money(a.AfterData)
			bv, _ := money(b.AfterData)
			pairReports[i] = paired{Phase: s.Phase, A: a.Model, B: b.Model, Point: av - bv, Intervals: []interval{}}
		}
		for _, block := range p.Blocks {
			ranges, deltas, e := bootstrap(ctx, series, fees, block, p.Replicates, p.Seed)
			if e != nil {
				return out, e
			}
			for i, q := range ranges {
				a := &out.Results[start+i]
				a.Intervals = append(a.Intervals, interval{block, q[0], q[1]})
			}
			for i, q := range deltas {
				pairReports[i].Intervals = append(pairReports[i].Intervals, interval{block, q[0], q[1]})
			}
		}
		out.Paired = append(out.Paired, pairReports...)
	}
	out.Code, out.Dirty = "unavailable", "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, v := range info.Settings {
			if v.Key == "vcs.revision" {
				out.Code = v.Value
			}
			if v.Key == "vcs.modified" {
				out.Dirty = v.Value
			}
		}
	}
	return out, nil
}

func saveNew(path string, value any) error {
	clean := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(clean, "data/research/") || !strings.HasSuffix(clean, ".json") || filepath.IsAbs(path) {
		return fail("Output must be a new relative data/research JSON file")
	}
	body, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return fail("Analysis report could not be encoded")
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return fail("Analysis output exists or cannot be created")
	}
	_, e = f.Write(body)
	if e == nil {
		e = f.Sync()
	}
	c := f.Close()
	if e != nil || c != nil {
		return fail("Analysis report could not be persisted")
	}
	return nil
}
func run(ctx context.Context, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("kabu-loss-analysis", flag.ContinueOnError)
	fs.SetOutput(w)
	input := fs.String("input", "", "Pinned exploratory analysis manifest; local files only")
	outputPath := fs.String("output", "", "New data/research JSON; existing files are preserved")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return fail("Invalid analysis arguments")
	}
	if *input == "" || *outputPath == "" || fs.NArg() != 0 {
		return fail("Input and output are required")
	}
	raw, e := readFile(*input)
	if e != nil {
		return e
	}
	var m manifest
	if e := decode(raw, &m); e != nil {
		return e
	}
	report, e := analyze(ctx, m, digest(raw))
	if e != nil {
		return e
	}
	if e := saveNew(*outputPath, report); e != nil {
		return e
	}
	fmt.Fprintf(w, "fixed_book_postmortem=PASS / books=%d / normalized_files=%d / bootstrap_replicates=%d / strategy_changed=false / strategy_adopted=false\n", len(report.Results), len(report.Files), report.Replicates)
	for _, a := range report.Results {
		p := a.Paths["status/executed"]
		q := a.Intervals[1]
		fmt.Fprintf(w, "phase=%s / model=%s / candidates=%d / executed=%d / stops=%d / later_entry_reached=%d / positive_deadline_after_stop=%d / conditional_5day_interval_jpy=[%.2f,%.2f]\n", a.Phase, a.Model, a.Groups["all"].Count, a.Executed, p.Stops, p.Recovered, p.RecoveredAtEnd, q.Low, q.High)
	}
	fmt.Fprintf(w, "saved=%s\n", *outputPath)
	return nil
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if e := run(ctx, os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
