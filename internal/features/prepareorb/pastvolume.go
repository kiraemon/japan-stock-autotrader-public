package prepareorb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

const PastVolumePolicy = "jquants-prior-empty-opening-daily-matched-zero-volume-v1"
const PastVolumeVersion = "stocks-in-play-input-v0.3"

// PastZeroEvidence is provenance of a provider-contract assumption, not an
// independently observed trading state. It never supplies a price or bar.
type PastZeroEvidence struct {
	Date           string   `json:"date_jst"`
	DaySHA         string   `json:"normalized_day_sha256"`
	DailySHA       string   `json:"daily_reference_sha256"`
	SourceSHA      string   `json:"minute_source_sha256"`
	OriginalIssues []string `json:"original_day_issues"`
	Assumption     string   `json:"assumption"`
}

type PastZeroFailure struct {
	Date    string   `json:"date_jst"`
	Reasons []string `json:"reasons"`
}

// PriorZeroVolumeEvidence cannot be populated through JSON or a generic bool.
// Only BuildPriorZeroVolumeEvidence can create its source-bound snapshots.
type PriorZeroVolumeEvidence struct {
	source   marketdata.Source
	symbol   string
	days     map[string]PastZeroEvidence
	rejected map[string][]string
}

func dayDigest(day marketdata.Day) string {
	b, _ := json.Marshal(day)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// BuildPriorZeroVolumeEvidence checks original-source day existence, closed
// bars/events, known issues and full-day OHLCV against actual daily references.
// An absent reference, mismatch or unknown issue supplies no zero assumption.
// It may index many dates, but CheckWithPriorZeroVolume only reads dates < T.
func BuildPriorZeroVolumeEvidence(ctx context.Context, d marketdata.Dataset, daily []marketdata.DailyBar) (PriorZeroVolumeEvidence, error) {
	e := PriorZeroVolumeEvidence{}
	if marketdata.Validate(d) != nil || d.Origin != "observed" || d.Environment != "production" || d.Source.Label != "start" || (d.Source.Format != "jquants-bulk-minute-v0.1" && d.Source.Format != marketdata.JQuantsJoinedFormat) || !strings.HasSuffix(d.Symbol, "@1") {
		return e, errors.New("過去出来高の証拠はJ-Quants一括分足と日足に限定しています")
	}
	if ctx.Err() != nil {
		return e, errors.New("過去出来高の照合を中断しました")
	}
	e = PriorZeroVolumeEvidence{source: d.Source, symbol: d.Symbol, days: map[string]PastZeroEvidence{}, rejected: map[string][]string{}}
	days := map[string]marketdata.Day{}
	for _, day := range d.Days {
		days[day.Date] = day
	}
	seen := map[string]bool{}
	for _, ref := range daily {
		if ctx.Err() != nil {
			return PriorZeroVolumeEvidence{}, errors.New("過去出来高の照合を中断しました")
		}
		date, err := time.Parse("2006-01-02", ref.Date)
		if err != nil || date.Format("2006-01-02") != ref.Date || ref.Code != strings.TrimSuffix(d.Symbol, "@1")+"0" || seen[ref.Date] {
			return PriorZeroVolumeEvidence{}, errors.New("同銘柄の重複しない日足参照を確認してください")
		}
		seen[ref.Date] = true
		day, exists := days[ref.Date]
		if !exists {
			e.rejected[ref.Date] = []string{"source_day_missing"}
			continue
		}
		if !ref.HasTrades || ref.Volume <= 0 {
			e.rejected[ref.Date] = []string{"daily_reference_without_positive_trading_volume"}
			continue
		}
		o := opening(d, ref.Date, JQuantsPrefixPolicy)
		if o.Volume != nil || len(o.MissingMinutes) != 5 || len(o.Problems) != 1 || o.Problems[0] != "missing_opening_minutes" {
			continue
		}
		clean := true
		for _, bar := range day.Bars {
			if len(bar.Issues) > 0 || bar.Volume == nil || *bar.Volume <= 0 {
				clean = false
			}
		}
		if !clean {
			e.rejected[ref.Date] = []string{"source_day_bar_issue_or_unknown_nonpositive_volume"}
			continue
		}
		check, err := marketdata.CheckJQuantsWindow(ctx, d, ref.Date, ref)
		if err != nil {
			return PriorZeroVolumeEvidence{}, err
		}
		if !check.DailyMatch {
			e.rejected[ref.Date] = append([]string{}, check.Reasons...)
			continue
		}
		for _, reason := range check.Reasons {
			switch reason {
			case "fixed_window_required_minute_missing", "current_day_action_basis_unverified":
				// This date is a historical raw-volume reference, never an entry
				// price window. Corporate-action notes remain with the caller.
			default:
				clean = false
			}
		}
		if !clean {
			e.rejected[ref.Date] = append([]string{}, check.Reasons...)
			continue
		}
		e.days[ref.Date] = PastZeroEvidence{Date: ref.Date, DaySHA: dayDigest(day), DailySHA: check.DailySHA, SourceSHA: d.Source.SHA256, OriginalIssues: append([]string{}, check.OriginalIssues...), Assumption: "prior_whole_opening_no_trades_assumed_under_provider_contract_not_independent_trade_status_proof"}
	}
	return e, nil
}

// CheckWithPriorZeroVolume keeps the fixed clock and all fourteen dates. Only
// fully absent *historical* volumes can be zero under the bound evidence. A
// target with no opening price range remains unavailable, and partial gaps
// retain their original treatment. Default Check/CheckWithPolicy are unchanged.
func CheckWithPriorZeroVolume(ctx context.Context, d marketdata.Dataset, date string, previous []string, evidence PriorZeroVolumeEvidence) (Report, error) {
	if evidence.days == nil || evidence.symbol != d.Symbol || evidence.source != d.Source {
		return Report{}, errors.New("同じ分足出典を検査した過去出来高の証拠を確認してください")
	}
	r, err := CheckWithPolicy(ctx, d, date, previous, JQuantsPrefixPolicy)
	if err != nil {
		return r, err
	}
	r.Version, r.OpeningPolicy = PastVolumeVersion, PastVolumePolicy
	r.CalculationAvailable = false
	r.PreviousMean, r.RelativeVolume, r.AtLeastOne = nil, nil, nil
	r.Problems, r.MissingPrevious = []string{}, []string{}
	r.PriorZeroEvidence = []PastZeroEvidence{}
	days := map[string]marketdata.Day{}
	for _, day := range d.Days {
		days[day.Date] = day
	}
	sum := new(big.Int)
	available := 0
	for i := range r.Previous {
		if ctx.Err() != nil {
			return r, errors.New("過去出来高の計算を中断しました")
		}
		o := &r.Previous[i]
		proof, ok := evidence.days[o.Date]
		if o.Volume == nil && len(o.MissingMinutes) == 5 && ok {
			day, exists := days[o.Date]
			if !exists || o.Date >= date || dayDigest(day) != proof.DaySHA {
				return r, errors.New("検査後の参照日/分足内容が変更されています")
			}
			zero := "0"
			o.OriginalProblems = append([]string{}, o.Problems...)
			o.Problems = []string{}
			o.Volume, o.Range = &zero, nil
			o.MissingInterpretation = "prior_whole_opening_assumed_zero_volume_no_price_range_created"
			proof.OriginalIssues = append([]string{}, proof.OriginalIssues...)
			r.PriorZeroEvidence = append(r.PriorZeroEvidence, proof)
		}
		if o.Volume == nil {
			if len(o.MissingMinutes) == 5 {
				reasons := append([]string{}, evidence.rejected[o.Date]...)
				if len(reasons) == 0 {
					reasons = append(reasons, "daily_reference_or_zero_volume_evidence_unavailable")
				}
				r.PriorZeroUnavailable = append(r.PriorZeroUnavailable, PastZeroFailure{Date: o.Date, Reasons: reasons})
			}
			r.MissingPrevious = append(r.MissingPrevious, o.Date)
			continue
		}
		v, ok := new(big.Int).SetString(*o.Volume, 10)
		if !ok || v.Sign() < 0 {
			return r, errors.New("過去出来高の非負整数を確認してください")
		}
		available++
		sum.Add(sum, v)
	}
	*r.AvailablePrevious = available
	if r.Target.Volume == nil {
		r.Problems = append(r.Problems, "target_opening_unavailable")
	}
	if available != Lookback {
		r.Problems = append(r.Problems, "previous_14_openings_incomplete")
	}
	if len(r.Problems) > 0 {
		return r, nil
	}
	if sum.Sign() == 0 {
		r.Problems = append(r.Problems, "zero_reference_volume")
		return r, nil
	}
	target, _ := new(big.Int).SetString(*r.Target.Volume, 10)
	mean := new(big.Rat).SetFrac(sum, big.NewInt(Lookback)).RatString()
	numerator := new(big.Int).Mul(target, big.NewInt(Lookback))
	ratio := new(big.Rat).SetFrac(numerator, sum).RatString()
	atLeast := numerator.Cmp(sum) >= 0
	r.CalculationAvailable, r.PreviousMean, r.RelativeVolume, r.AtLeastOne = true, &mean, &ratio, &atLeast
	return r, nil
}
