// Package observespread describes archived quote samples at their receipt time.
// It has no execution, I/O, credential or live risk capability.
package observespread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"sort"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

const Version = "receipt-sampled-quoted-spread-v0.1"

type Input struct {
	Quote       marketdata.Quote
	SnapshotSHA string
	RecordIssue bool
}

type Distribution struct {
	Samples int    `json:"samples"`
	Minimum string `json:"minimum_exact"`
	Median  string `json:"median_exact"`
	P90     string `json:"p90_nearest_rank_exact"`
	Maximum string `json:"maximum_exact"`
}

type Group struct {
	Date              string         `json:"date_jst"`
	Symbol            string         `json:"symbol"`
	Bucket            string         `json:"received_time_bucket_jst"`
	Samples           int            `json:"samples"`
	StructurallyValid int            `json:"structurally_valid_continuous_session"`
	DuplicateBBO      int            `json:"duplicate_bbo_samples"`
	Fresh2            int            `json:"fresh_within_2s"`
	Fresh5            int            `json:"fresh_within_5s"`
	Fresh30           int            `json:"fresh_within_30s"`
	UniqueFresh2      int            `json:"unique_bbo_fresh_within_2s"`
	Yen               *Distribution  `json:"fresh_2s_full_spread_jpy,omitempty"`
	BPS               *Distribution  `json:"fresh_2s_full_spread_mid_bps,omitempty"`
	UniqueBPS         *Distribution  `json:"unique_bbo_fresh_2s_full_spread_mid_bps,omitempty"`
	Issues            map[string]int `json:"issue_counts"`
	yen, bps, unique  []*big.Rat
}

type Observation struct {
	Symbol      string    `json:"symbol"`
	Received    time.Time `json:"received_at"`
	BuyTime     time.Time `json:"best_buy_market_time"`
	SellTime    time.Time `json:"best_sell_market_time"`
	SourceSHA   string    `json:"board_sha256"`
	SnapshotSHA string    `json:"snapshot_sha256"`
	Issues      []string  `json:"issues"`
	Duplicate   bool      `json:"duplicate_bbo"`
	Fresh2      bool      `json:"fresh_at_receipt_within_2s"`
	Yen         string    `json:"full_spread_jpy_exact,omitempty"`
	BPS         string    `json:"full_spread_mid_bps_exact,omitempty"`
}

type Report struct {
	Version           string        `json:"version"`
	InspectionAt      time.Time     `json:"inspection_at"`
	Samples           int           `json:"samples"`
	StructurallyValid int           `json:"structurally_valid_continuous_session"`
	Fresh2            int           `json:"fresh_within_2s"`
	Groups            []Group       `json:"groups"`
	Observations      []Observation `json:"observations"`
	Limitations       []string      `json:"limitations"`
	ForwardTest       bool          `json:"strategy_forward_test"`
	ExecutionIncluded bool          `json:"execution_included"`
	Trading           bool          `json:"trading_eligible"`
	Quality           bool          `json:"quality_promoted"`
}

var symbol = regexp.MustCompile(`^[0-9A-Z]{4}@1$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func session(at time.Time) string {
	t := at.In(marketdata.JST)
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday || marketdata.Date(t) < "2024-11-05" {
		return ""
	}
	minute := t.Hour()*60 + t.Minute()
	if minute >= 540 && minute < 690 {
		return "am"
	}
	if minute >= 750 && minute < 925 {
		return "pm"
	}
	return ""
}

func bucket(at time.Time) string {
	t := at.In(marketdata.JST)
	if session(t) == "" {
		return "outside_continuous_session"
	}
	return t.Truncate(30 * time.Minute).Format("15:04")
}

func distribution(values []*big.Rat) *Distribution {
	if len(values) == 0 {
		return nil
	}
	copy := append([]*big.Rat{}, values...)
	sort.Slice(copy, func(i, j int) bool { return copy[i].Cmp(copy[j]) < 0 })
	median := new(big.Rat).Set(copy[len(copy)/2])
	if len(copy)%2 == 0 {
		median.Add(median, copy[len(copy)/2-1])
		median.Quo(median, big.NewRat(2, 1))
	}
	p90 := (9*len(copy)+9)/10 - 1
	return &Distribution{len(copy), copy[0].RatString(), median.RatString(), copy[p90].RatString(), copy[len(copy)-1].RatString()}
}

// Analyze does not claim today's freshness for an archived quote. Freshness is
// measured against each original receipt, with inspection only an upper bound.
// All-sample and unique-BBO distributions are both kept; neither is time weighted.
func Analyze(ctx context.Context, input []Input, inspection time.Time) (Report, error) {
	r := Report{Version: Version, InspectionAt: inspection, Samples: len(input), Groups: []Group{}, Observations: []Observation{}, Limitations: []string{"archived_receipt_time_observation_not_current_quote_or_strategy_forward_test", "fixed_primary_freshness_2s_and_5s_30s_sensitivity_not_live_limits", "general_quotes_only_no_fill_queue_or_inventory_guarantee", "quoted_full_spread_is_not_realized_slippage_or_round_trip_execution", "receipt_sample_weighted_and_unique_update_weighted_not_time_weighted", "tick_size_unavailable_without_historical_tick_group_or_constituent_evidence", "weekday_session_filter_does_not_verify_exchange_holidays", "three_timestamps_preserved_no_publication_time_or_latency_claim", "invalid_and_duplicate_observations_retained_no_quote_or_quality_promotion"}}
	if inspection.IsZero() || len(input) == 0 || len(input) > 100000 {
		return r, errors.New("気配観測の件数/検査時刻を確認してください")
	}
	rows := append([]Input{}, input...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Quote, rows[j].Quote
		if !a.ReceivedAt.Equal(b.ReceivedAt) {
			return a.ReceivedAt.Before(b.ReceivedAt)
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.SourceSHA256 != b.SourceSHA256 {
			return a.SourceSHA256 < b.SourceSHA256
		}
		return rows[i].SnapshotSHA < rows[j].SnapshotSHA
	})
	groups := map[string]*Group{}
	seen := map[string]bool{}
	for _, v := range rows {
		if ctx.Err() != nil {
			return r, errors.New("気配観測の集計を中断しました")
		}
		q := v.Quote
		if !symbol.MatchString(q.Symbol) || !digest.MatchString(v.SnapshotSHA) {
			return r, errors.New("気配観測の銘柄/原スナップショットhashを確認してください")
		}
		date := marketdata.Date(q.ReceivedAt)
		key := date + "/" + q.Symbol + "/" + bucket(q.ReceivedAt)
		g := groups[key]
		if g == nil {
			g = &Group{Date: date, Symbol: q.Symbol, Bucket: bucket(q.ReceivedAt), Issues: map[string]int{}}
			groups[key] = g
		}
		g.Samples++
		o := Observation{Symbol: q.Symbol, Received: q.ReceivedAt, BuyTime: q.Bid.Time, SellTime: q.Ask.Time, SourceSHA: q.SourceSHA256, SnapshotSHA: v.SnapshotSHA, Issues: []string{}}
		if !digest.MatchString(q.SourceSHA256) {
			o.SourceSHA = ""
		}
		add := func(reason string) { o.Issues = append(o.Issues, reason); g.Issues[reason]++ }
		if v.RecordIssue {
			add("snapshot_or_board_record_issue")
		}
		if q.Version != marketdata.QuoteVersion || q.Origin != "observed" || q.Environment != "production" || !digest.MatchString(q.SourceSHA256) {
			add("quote_contract_or_source_unverified")
		}
		if len(q.Issues) > 0 {
			add("normalization_quality_issue")
		}
		if q.ReceivedAt.IsZero() || q.ReceivedAt.After(inspection) {
			add("receipt_time_missing_or_after_inspection")
		}
		if session(q.ReceivedAt) == "" {
			add("receipt_outside_supported_continuous_session")
		}
		if q.Bid.Price <= 0 || q.Ask.Price <= q.Bid.Price || q.Ask.Price > marketdata.MaxPrice {
			add("prices_unknown_locked_or_crossed")
		}
		if q.Bid.Quantity == nil || q.Ask.Quantity == nil || q.Bid.Quantity != nil && *q.Bid.Quantity <= 0 || q.Ask.Quantity != nil && *q.Ask.Quantity <= 0 {
			add("displayed_quantity_unknown_or_nonpositive")
		}
		for _, side := range []marketdata.QuoteSide{q.Bid, q.Ask} {
			if side.Sign != "0101" {
				add("not_general_quote")
				break
			}
		}
		for _, side := range []marketdata.QuoteSide{q.Bid, q.Ask} {
			if side.Time.IsZero() || side.Time.After(q.ReceivedAt) || marketdata.Date(side.Time) != date || session(side.Time) == "" || session(side.Time) != session(q.ReceivedAt) {
				add("side_time_unknown_future_or_session_mismatch")
				break
			}
		}
		if len(o.Issues) == 0 {
			r.StructurallyValid++
			g.StructurallyValid++
			spread := q.Ask.Price - q.Bid.Price
			yen := big.NewRat(spread, marketdata.PriceScale)
			bps := big.NewRat(spread*20000, q.Ask.Price+q.Bid.Price)
			o.Yen, o.BPS = yen.RatString(), bps.RatString()
			// Time.MarshalJSON canonicalises differing equivalent timezone labels.
			a, b := q.Bid, q.Ask
			a.Time, b.Time = a.Time.UTC(), b.Time.UTC()
			body, _ := json.Marshal(struct {
				Symbol   string
				Bid, Ask marketdata.QuoteSide
			}{q.Symbol, a, b})
			h := sha256.Sum256(body)
			fingerprint := hex.EncodeToString(h[:])
			o.Duplicate = seen[fingerprint]
			seen[fingerprint] = true
			if o.Duplicate {
				g.DuplicateBBO++
			}
			age := q.ReceivedAt.Sub(q.Bid.Time)
			if other := q.ReceivedAt.Sub(q.Ask.Time); other > age {
				age = other
			}
			if age <= 30*time.Second {
				g.Fresh30++
			}
			if age <= 5*time.Second {
				g.Fresh5++
			}
			if age <= 2*time.Second {
				o.Fresh2 = true
				r.Fresh2++
				g.Fresh2++
				g.yen = append(g.yen, yen)
				g.bps = append(g.bps, bps)
				if !o.Duplicate {
					g.UniqueFresh2++
					g.unique = append(g.unique, bps)
				}
			} else {
				add("older_than_primary_2s_at_receipt")
			}
		}
		r.Observations = append(r.Observations, o)
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		g := groups[key]
		g.Yen, g.BPS, g.UniqueBPS = distribution(g.yen), distribution(g.bps), distribution(g.unique)
		g.yen, g.bps, g.unique = nil, nil, nil
		r.Groups = append(r.Groups, *g)
	}
	return r, nil
}
