// Package streambars audits successive complete history responses without
// concatenating trades or claiming that elapsed time proves completeness.
package streambars

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

const Version = "history-minute-candidates-v0.1"

// These are diagnostic comparisons with the existing synthetic feed contract,
// not operational latency guarantees or a source-quality override.
const CloseLag = 2 * time.Second
const ArrivalDeadline = 10 * time.Second

type Config struct {
	Date        string `json:"date_jst"`
	Symbol      string `json:"symbol"`
	Environment string `json:"environment"`
}

type Candidate struct {
	Bar              marketdata.Bar `json:"bar"`
	ReceivedAt       time.Time      `json:"received_at"`
	ObservedAt       time.Time      `json:"observed_at"`
	SourceSHA256     string         `json:"source_sha256"`
	Issues           []string       `json:"issues"`
	Late             bool           `json:"late_under_synthetic_deadline"`
	StrategyEligible bool           `json:"strategy_eligible"` // Always false.
}

type Outcome struct {
	Accepted   bool        `json:"accepted"`
	Reason     string      `json:"reason,omitempty"`
	Duplicates int         `json:"duplicate_minutes"`
	Candidates []Candidate `json:"candidates"`
}

type State struct {
	Version    string    `json:"version"`
	Config     Config    `json:"config"`
	Frames     int       `json:"accepted_frames"`
	Candidates int       `json:"candidate_minutes"`
	Late       int       `json:"late_minutes"`
	NextStart  time.Time `json:"next_minute_start"`
	Complete   bool      `json:"window_enumerated"` // Not source validation.
	Halted     bool      `json:"halted"`
	StopReason string    `json:"stop_reason,omitempty"`
	LiveReady  bool      `json:"live_ready"` // Always false.
}

type Feed struct {
	state               State
	start, lastClock    time.Time
	lastReceived        time.Time
	lastHash, converter string
	seen                map[time.Time][32]byte
}

func New(config Config) (*Feed, error) {
	date, err := time.ParseInLocation("2006-01-02", config.Date, marketdata.JST)
	probe := marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: config.Symbol, Environment: config.Environment, Origin: "observed", Converter: Version, Source: marketdata.Source{Format: "kabu-timeandsales", SHA256: strings.Repeat("0", 64), Label: "start", ReceivedAt: date}, Days: []marketdata.Day{{Date: config.Date}}}
	if err != nil || marketdata.Validate(probe) != nil {
		return nil, errors.New("履歴分足検査の日付・銘柄・環境を確認してください")
	}
	start := marketdata.Clock(config.Date, 9, 0)
	return &Feed{state: State{Version: Version, Config: config, NextStart: start}, start: start, seen: map[time.Time][32]byte{}}, nil
}

func (f *Feed) halt(reason string) Outcome {
	if !f.state.Halted {
		f.state.Halted, f.state.StopReason = true, reason
	}
	return Outcome{Reason: f.state.StopReason, Candidates: []Candidate{}}
}

// Offer consumes one independently converted response in caller-supplied order.
// now is the actual observation time in a future runner; offline audits must
// explicitly label any replay of archive finished_at as a historical assumption.
// A bad frame commits no candidates. Source/day/bar issues remain attached and
// every candidate is ineligible for strategy delivery, including issue-free ones.
func (f *Feed) Offer(now time.Time, d marketdata.Dataset) Outcome {
	if f.state.Halted {
		return f.halt(f.state.StopReason)
	}
	if now.IsZero() || marketdata.Date(now) != f.state.Config.Date || now.Before(f.start) || !f.lastClock.IsZero() && now.Before(f.lastClock) {
		return f.halt("observation_clock_reversed_or_invalid")
	}
	f.lastClock = now
	if marketdata.Validate(d) != nil || d.Symbol != f.state.Config.Symbol || d.Environment != f.state.Config.Environment || d.Origin != "observed" || d.Source.Format != "kabu-timeandsales" || d.Source.Label != "start" || f.converter != "" && d.Converter != f.converter {
		return f.halt("history_identity_or_contract_invalid")
	}
	received := d.Source.ReceivedAt
	if received.After(now) || marketdata.Date(received) != f.state.Config.Date || !f.lastReceived.IsZero() && received.Before(f.lastReceived) {
		return f.halt("history_received_time_invalid")
	}
	if received.Equal(f.lastReceived) && d.Source.SHA256 != f.lastHash {
		return f.halt("history_time_reused_with_changed_source")
	}
	var day marketdata.Day
	for _, item := range d.Days {
		if item.Date == f.state.Config.Date {
			day = item
		}
	}
	closed := map[time.Time]marketdata.Bar{}
	endWindow := f.start.Add(111 * time.Minute)
	for _, b := range day.Bars {
		key := b.Start.UTC()
		if key.Before(f.start) || !key.Before(endWindow) {
			continue
		}
		if !b.FirstTime.IsZero() && b.FirstTime.After(received) || !b.LastTime.IsZero() && b.LastTime.After(received) {
			return f.halt("history_market_time_in_future")
		}
		if received.Before(key.Add(time.Minute + CloseLag)) {
			continue
		}
		closed[key] = b
	}
	duplicates := 0
	// Check earlier candidates first so multiple faults have a stable reason;
	// Go map iteration must not choose which incident is reported.
	for i := 0; i < f.state.Candidates; i++ {
		key := f.start.Add(time.Duration(i) * time.Minute).UTC()
		previous := f.seen[key]
		b, exists := closed[key]
		if !exists {
			return f.halt("previous_candidate_missing")
		}
		if fingerprint(b, day.Issues) != previous {
			return f.halt("candidate_minute_revised")
		}
		duplicates++
	}
	// Enumerate from the expected minute; neither an empty minute nor a zero
	// volume candle is fabricated. Late history is kept as late diagnostic data.
	candidates := []Candidate{}
	next := f.state.NextStart
	for next.Before(endWindow) && !received.Before(next.Add(time.Minute+CloseLag)) {
		b, exists := closed[next.UTC()]
		if !exists {
			return f.halt("closed_minute_missing")
		}
		issues := append(slices.Clone(day.Issues), b.Issues...)
		issues = append(issues, "source_validation_pending")
		late := now.After(next.Add(time.Minute + ArrivalDeadline))
		if late {
			issues = append(issues, "arrival_deadline_exceeded")
		}
		b.Volume = cloneQuantity(b.Volume)
		b.Issues = slices.Clone(b.Issues)
		candidates = append(candidates, Candidate{Bar: b, ReceivedAt: received, ObservedAt: now, SourceSHA256: d.Source.SHA256, Issues: issues, Late: late})
		next = next.Add(time.Minute)
	}
	for _, c := range candidates {
		f.seen[c.Bar.Start.UTC()] = fingerprint(c.Bar, day.Issues)
		if c.Late {
			f.state.Late++
		}
	}
	f.lastReceived, f.lastHash, f.converter = received, d.Source.SHA256, d.Converter
	f.state.Frames++
	f.state.Candidates += len(candidates)
	f.state.NextStart = next
	f.state.Complete = next.Equal(endWindow)
	return Outcome{Accepted: true, Duplicates: duplicates, Candidates: candidates}
}

func fingerprint(b marketdata.Bar, issues []string) [32]byte {
	quality := append(slices.Clone(issues), b.Issues...)
	slices.Sort(quality)
	quality = slices.Compact(quality)
	raw, _ := json.Marshal(struct {
		Start                  time.Time
		Open, High, Low, Close int64
		Volume                 *int64
		Issues                 []string
	}{b.Start.UTC(), b.Open, b.High, b.Low, b.Close, b.Volume, quality})
	return sha256.Sum256(raw)
}

func cloneQuantity(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func (f *Feed) Snapshot() State { return f.state }
