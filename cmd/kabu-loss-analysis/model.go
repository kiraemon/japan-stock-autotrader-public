// kabu-loss-analysis reads immutable research artifacts for descriptive loss
// analysis. It imports no broker, strategy, feature or production book code.
package main

import (
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"time"
)

const version = "round3-fixed-book-postmortem-v1"
const scopeSHA = "05f28d1bf6511ce124183cfbc5ff0e03a522e4b9d6259bf5d7e70a21786ce49d"

var models = []string{"S01-JP20-v0.1", "NA30-JP-v0.1", "GAP29-JP-v0.1"}
var jst = time.FixedZone("JST", 9*3600)
var number = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(/[1-9][0-9]*)?$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func fail(s string) error { return errors.New(s + "; input values withheld") }
func exact(s string) (*big.Rat, error) {
	if len(s) > 256 || !number.MatchString(s) {
		return nil, fail("Invalid exact monetary field")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fail("Invalid monetary denominator")
	}
	return r, nil
}
func money(s string) (float64, error) {
	v, e := exact(s)
	if e != nil {
		return 0, e
	}
	f, _ := v.Float64()
	return f, nil
}
func clock(date string, m int) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", date, jst)
	return t.Add(time.Duration(m) * time.Minute)
}

type pin struct {
	Path string `json:"path"`
	SHA  string `json:"sha256"`
}
type manifest struct {
	Version int   `json:"version"`
	Plan    pin   `json:"plan"`
	Input   pin   `json:"study_manifest"`
	Studies []pin `json:"studies"`
}
type plan struct {
	Version    int      `json:"version"`
	ID         string   `json:"analysis_id"`
	Models     []string `json:"models"`
	Replicates int      `json:"bootstrap_replicates"`
	Seed       uint64   `json:"bootstrap_seed"`
	Blocks     []int    `json:"bootstrap_block_lengths"`
	Primary    int      `json:"bootstrap_primary_block_length"`
	Orders     bool     `json:"real_orders_enabled"`
}
type scenario struct {
	BPS        int    `json:"round_trip_friction_bps"`
	Commission string `json:"commission_scenario"`
	Extra      int    `json:"short_extra_jpy"`
	Common     bool   `json:"common_cases_only"`
}
type trade struct {
	Direction string    `json:"direction"`
	EntryAt   time.Time `json:"entry_at"`
	ExitAt    time.Time `json:"exit_at"`
	Entry     int64     `json:"entry_price_scaled"`
	Exit      int64     `json:"exit_price_scaled"`
	Reason    string    `json:"exit_reason"`
}
type costs struct {
	Gross  string `json:"gross_jpy_exact"`
	Net    string `json:"net_before_data_jpy_exact"`
	Budget string `json:"entry_budget_jpy_ceil"`
}
type entry struct {
	Date   string `json:"date_jst"`
	Symbol string `json:"symbol"`
	Rank   int    `json:"liquidity_rank"`
	Status string `json:"status"`
	Trade  trade  `json:"trade"`
	Costs  costs  `json:"costs"`
	Fits   bool   `json:"fits_independent_400000_jpy_budget"`
}
type book struct {
	Version     string   `json:"version"`
	Scenario    scenario `json:"scenario"`
	Cases       int      `json:"cases"`
	Unevaluable int      `json:"unevaluable_cases"`
	Candidates  int      `json:"candidates"`
	Executed    int      `json:"executed"`
	Gross       string   `json:"gross_jpy_exact"`
	Net         string   `json:"net_before_data_jpy_exact"`
	After       string   `json:"net_after_data_jpy_exact"`
	Data        string   `json:"allocated_data_fee_jpy_exact"`
	Ledger      []entry  `json:"ledger"`
	Trading     bool     `json:"trading_eligible"`
	Adopted     bool     `json:"strategy_adopted"`
}
type bookRef struct {
	Model    string `json:"model"`
	Phase    string `json:"partition"`
	Cohort   string `json:"comparison_cohort"`
	Artifact pin    `json:"artifact"`
	Summary  book   `json:"summary"`
}
type study struct {
	Version     string    `json:"version"`
	Phase       string    `json:"phase"`
	ManifestSHA string    `json:"manifest_sha256"`
	ScopeSHA    string    `json:"strategy_plan_sha256"`
	IndexSHA    string    `json:"normalized_index_sha256"`
	Dates       []string  `json:"evaluated_sessions"`
	Books       []bookRef `json:"books"`
	Trading     bool      `json:"trading_eligible"`
	Adopted     bool      `json:"strategy_adopted"`
}
type bar struct {
	At                     time.Time `json:"start"`
	Open, High, Low, Close int64
	Issues                 []string `json:"issues"`
}
type day struct {
	Date string `json:"date_jst"`
	Bars []bar  `json:"bars"`
}
type dataset struct {
	Version     int    `json:"schema_version"`
	Scale       int64  `json:"price_scale"`
	Symbol      string `json:"symbol"`
	Origin      string `json:"origin"`
	Environment string `json:"environment"`
	Source      struct {
		Format string `json:"format"`
		SHA    string `json:"sha256"`
		Label  string `json:"time_label"`
	} `json:"source"`
	Days []day `json:"days"`
}
type minuteFile struct {
	Phase     string `json:"partition"`
	Month     string `json:"month"`
	Symbol    string `json:"symbol"`
	Path      string `json:"path"`
	SHA       string `json:"sha256"`
	SourceSHA string `json:"source_sha256"`
}
type inputManifest struct {
	Index pin `json:"normalized_index"`
	Scope pin `json:"scope"`
}
type normalizedIndex struct {
	Records  []minuteFile `json:"records"`
	Complete bool         `json:"complete"`
	PNL      bool         `json:"candidate_pnl_evaluated"`
	Quality  bool         `json:"quality_promoted"`
}

// Reports contain derived statistics, never individual source OHLC prices.
type group struct {
	Count                     int     `json:"candidates"`
	GrossMean                 float64 `json:"mean_gross_jpy"`
	NetMean                   float64 `json:"mean_individual_net_before_data_jpy"`
	GrossBPSMean              float64 `json:"mean_gross_bps"`
	NotionalMean              float64 `json:"mean_entry_notional_jpy"`
	Fits                      int     `json:"independent_budget_fit"`
	gross, net, bps, notional float64
}

func (g *group) add(e entry) error {
	gross, er := money(e.Costs.Gross)
	if er != nil {
		return er
	}
	net, er := money(e.Costs.Net)
	if er != nil {
		return er
	}
	g.Count++
	g.gross += gross
	g.net += net
	g.notional += float64(e.Trade.Entry) / 100
	g.bps += gross / (float64(e.Trade.Entry) / 100) * 10000
	if e.Fits {
		g.Fits++
	}
	g.GrossMean = g.gross / float64(g.Count)
	g.NetMean = g.net / float64(g.Count)
	g.GrossBPSMean = g.bps / float64(g.Count)
	g.NotionalMean = g.notional / float64(g.Count)
	return nil
}

type pathSummary struct {
	EarlyGapCount       int     `json:"gap_first_minute_observations"`
	EarlyGapMean        float64 `json:"gap_first_minute_mean_signed_bps_not_assumed_fill"`
	EarlyGapSum         float64 `json:"gap_first_minute_signed_jpy_sum_not_assumed_profit"`
	OvershootCount      int     `json:"s01_boundary_gap_observations"`
	OvershootMean       float64 `json:"s01_mean_signed_entry_boundary_gap_bps"`
	earlyGap, overshoot float64
	Count               int     `json:"candidates"`
	MFE                 float64 `json:"mean_fixed_horizon_mfe_bps"`
	MAE                 float64 `json:"mean_fixed_horizon_mae_bps"`
	Endpoint            float64 `json:"mean_deadline_mark_bps"`
	PositivePeak        int     `json:"positive_favorable_extreme"`
	PositiveEndpoint    int     `json:"positive_deadline_mark"`
	Stops               int     `json:"original_stops"`
	Recovered           int     `json:"post_stop_entry_reached_excluding_ambiguous_bar"`
	RecoveredAtEnd      int     `json:"positive_deadline_mark_after_stop"`
	EndpointMinusExit   float64 `json:"stopped_deadline_minus_original_exit_gross_jpy_sum"`
	mfe, mae, endpoint  float64
}

func (p *pathSummary) add(x excursion) {
	if x.EarlyGapAvailable {
		p.EarlyGapCount++
		p.earlyGap += x.EarlyGapBPS
		p.EarlyGapMean = p.earlyGap / float64(p.EarlyGapCount)
		p.EarlyGapSum += x.EarlyGapJPY
	}
	if x.OvershootAvailable {
		p.OvershootCount++
		p.overshoot += x.EntryOvershootBPS
		p.OvershootMean = p.overshoot / float64(p.OvershootCount)
	}
	p.Count++
	p.mfe += x.MFE
	p.mae += x.MAE
	p.endpoint += x.Endpoint
	p.MFE = p.mfe / float64(p.Count)
	p.MAE = p.mae / float64(p.Count)
	p.Endpoint = p.endpoint / float64(p.Count)
	if x.MFE > 0 {
		p.PositivePeak++
	}
	if x.Endpoint > 0 {
		p.PositiveEndpoint++
	}
	if x.Stop {
		p.Stops++
		if x.Recovered {
			p.Recovered++
		}
		if x.Endpoint > 0 {
			p.RecoveredAtEnd++
		}
		p.EndpointMinusExit += x.EndpointDeltaJPY
	}
}

type interval struct {
	Block int     `json:"block_sessions"`
	Low   float64 `json:"percentile_025_total_jpy"`
	High  float64 `json:"percentile_975_total_jpy"`
}
type analysis struct {
	Phase       string                 `json:"phase"`
	Model       string                 `json:"model"`
	Root        pin                    `json:"study"`
	Book        pin                    `json:"book"`
	Sessions    int                    `json:"sessions"`
	Cases       int                    `json:"cases"`
	Unevaluable int                    `json:"unevaluable_cases_retained"`
	Executed    int                    `json:"executed"`
	ActiveDays  int                    `json:"days_with_execution"`
	AfterData   string                 `json:"original_net_after_data_jpy_exact"`
	Groups      map[string]group       `json:"candidate_groups"`
	Paths       map[string]pathSummary `json:"fixed_original_horizon_price_diagnostics"`
	Intervals   []interval             `json:"conditional_cashflow_bootstrap"`
	Daily       map[string]string      `json:"frozen_realized_net_before_data_by_day_exact"`
	series      []float64
	fee         float64
}
type paired struct {
	Phase     string     `json:"phase"`
	A         string     `json:"model_a"`
	B         string     `json:"model_b"`
	Point     float64    `json:"a_minus_b_original_total_jpy"`
	Intervals []interval `json:"paired_cashflow_bootstrap"`
}
type output struct {
	Version     string     `json:"version"`
	ManifestSHA string     `json:"manifest_sha256"`
	Plan        pin        `json:"plan"`
	Code        string     `json:"analysis_code_revision"`
	Dirty       string     `json:"analysis_working_tree_at_build"`
	Studies     []pin      `json:"studies"`
	Results     []analysis `json:"results"`
	Paired      []paired   `json:"paired_differences"`
	Files       []pin      `json:"normalized_files_read"`
	Replicates  int        `json:"bootstrap_replicates"`
	Seed        uint64     `json:"bootstrap_seed"`
	Limitations []string   `json:"limitations"`
	Adopted     bool       `json:"strategy_adopted"`
	NewStrategy bool       `json:"new_strategy_tested"`
}

func decode(b []byte, v any) error {
	if json.Unmarshal(b, v) != nil {
		return fail("Local JSON shape invalid")
	}
	return nil
}
