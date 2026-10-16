package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

const DiagnosticVersion = "crosschecked-price-pattern-v0.1"
const diagnosticMinutes = 111 // 09:00 through 10:50, including latest time exit.

type PriceDiagnostic struct {
	Version                 string          `json:"version"`
	CSVSourceSHA256         string          `json:"csv_source_sha256"`
	HistorySourceSHA256     string          `json:"history_source_sha256"`
	CSVNormalizedSHA256     string          `json:"csv_normalized_sha256"`
	HistoryNormalizedSHA256 string          `json:"history_normalized_sha256"`
	WindowStart             string          `json:"window_start_jst"`
	WindowLastBar           string          `json:"window_last_bar_start_jst"`
	Limitations             []string        `json:"limitations"`
	Days                    []PriceDayCheck `json:"days"`
}

type PriceDayCheck struct {
	Date             string   `json:"date_jst"`
	Required         int      `json:"required_minutes"`
	Matched          int      `json:"matched_minutes"`
	Eligible         bool     `json:"eligible_for_price_diagnostic"`
	Reasons          []string `json:"reasons"`
	CSVDayIssues     []string `json:"original_csv_day_issues"`
	HistoryDayIssues []string `json:"original_history_day_issues"`
}

// Diagnose uses a fixed, full rule window to avoid admitting an incomplete day
// only because its early trade happens to have an available exit. The input
// datasets are not mutated. This does not verify trading status or execution.
func Diagnose(ctx context.Context, csv, history marketdata.Dataset, direction r001.Direction) (Report, error) {
	for _, d := range []marketdata.Dataset{csv, history} {
		if err := marketdata.Validate(d); err != nil {
			return Report{}, err
		}
	}
	if csv.Origin != "observed" || history.Origin != "observed" || csv.Symbol != history.Symbol || csv.Environment != history.Environment || csv.Source.Label != "start" || history.Source.Label != "start" || (csv.Source.Format != "kabu-chart-csv-cp932" && csv.Source.Format != "kabu-chart-csv-utf8") || history.Source.Format != "kabu-timeandsales" || csv.Source.SHA256 == history.Source.SHA256 {
		return Report{}, errors.New("診断は同銘柄・市場・環境の開始ラベルのkabu元CSVと歩み値を照合してください")
	}
	csvHash, err := datasetHash(csv)
	if err != nil {
		return Report{}, err
	}
	historyHash, err := datasetHash(history)
	if err != nil {
		return Report{}, err
	}
	evidence := &PriceDiagnostic{Version: DiagnosticVersion, CSVSourceSHA256: csv.Source.SHA256, HistorySourceSHA256: history.Source.SHA256, CSVNormalizedSHA256: csvHash, HistoryNormalizedSHA256: historyHash, WindowStart: "09:00", WindowLastBar: "10:50", Days: []PriceDayCheck{}, Limitations: []string{
		"same_provider_crosscheck_only", "trading_status_unverified", "full_tick_sequence_unverified", "next_minute_open_execution_assumed", "short_inventory_and_actual_costs_unverified", "pilot_sample_not_strategy_validation",
	}}
	refs := map[string]marketdata.Day{}
	for _, day := range history.Days {
		refs[day.Date] = day
	}
	assessments := map[string][]string{}
	for _, day := range csv.Days {
		if ctx.Err() != nil {
			return Report{}, errors.New("価格パターン診断を中断しました")
		}
		check := PriceDayCheck{Date: day.Date, Required: diagnosticMinutes, Reasons: []string{}, CSVDayIssues: append([]string{}, day.Issues...)}
		ref, exists := refs[day.Date]
		check.HistoryDayIssues = append([]string{}, ref.Issues...)
		if !exists {
			check.Reasons = append(check.Reasons, "reference_day_missing")
		}
		if !knownDayIssues(day.Issues) || !knownDayIssues(ref.Issues) {
			check.Reasons = append(check.Reasons, "unsupported_day_quality_issue")
		}
		left, right := minutes(day), minutes(ref)
		start := marketdata.Clock(day.Date, 9, 0)
		for m := 0; m < diagnosticMinutes; m++ {
			if ctx.Err() != nil {
				return Report{}, errors.New("価格パターン診断を中断しました")
			}
			at := start.Add(time.Duration(m) * time.Minute).UTC()
			a, aOK := left[at]
			b, bOK := right[at]
			reason := ""
			switch {
			case !aOK || !bOK:
				reason = "required_crosscheck_minute_missing"
			case at.Add(time.Minute).After(csv.Source.ReceivedAt) || at.Add(time.Minute).After(history.Source.ReceivedAt):
				reason = "required_crosscheck_minute_unfinished"
			case len(a.Issues) > 0 || !knownHistoryBarIssues(b.Issues):
				reason = "unsupported_minute_quality_issue"
			case a.Volume == nil || b.Volume == nil || *a.Volume <= 0 || *b.Volume <= 0:
				reason = "required_crosscheck_volume_unknown_or_zero"
			case a.Open != b.Open || a.High != b.High || a.Low != b.Low || a.Close != b.Close || *a.Volume != *b.Volume:
				reason = "required_crosscheck_ohlcv_mismatch"
			default:
				check.Matched++
			}
			if reason != "" && !contains(check.Reasons, reason) {
				check.Reasons = append(check.Reasons, reason)
			}
		}
		check.Eligible = len(check.Reasons) == 0 && check.Matched == check.Required
		assessments[day.Date] = check.Reasons
		evidence.Days = append(evidence.Days, check)
	}
	r, err := run(ctx, csv, direction, assessments)
	if err != nil {
		return r, err
	}
	r.Version = 2
	r.Scope = "crosschecked_price_pattern_diagnostic"
	r.Diagnostic = evidence
	return r, nil
}

func minutes(day marketdata.Day) map[time.Time]marketdata.Bar {
	r := map[time.Time]marketdata.Bar{}
	for _, b := range day.Bars {
		r[b.Start.UTC()] = b
	}
	return r
}
func knownDayIssues(issues []string) bool {
	for _, issue := range issues {
		switch issue {
		case "coverage_unverified", "quantity_semantics_unverified", "trading_status_unverified", "source_crosscheck_unverified", "csv_semantics_unverified":
		default:
			return false
		}
	}
	return true
}
func knownHistoryBarIssues(issues []string) bool {
	for _, issue := range issues {
		if issue != "open_order_unverified" && issue != "close_order_unverified" {
			return false
		}
	}
	return true
}
func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
func datasetHash(d marketdata.Dataset) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", errors.New("正規化した分足の出典ハッシュを生成できません")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
