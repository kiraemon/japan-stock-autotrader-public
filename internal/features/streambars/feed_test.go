package streambars

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

const date = "2026-10-16"

func fixture(received time.Time, count int) marketdata.Dataset {
	volume := int64(100)
	bars := []marketdata.Bar{}
	for i := 0; i < count; i++ {
		bars = append(bars, marketdata.Bar{Start: marketdata.Clock(date, 9, i).UTC(), Open: 100, High: 101, Low: 99, Close: 100, Volume: &volume})
	}
	return marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: "7203@1", Environment: "production", Origin: "observed", Converter: "fixture-v1", Source: marketdata.Source{Format: "kabu-timeandsales", SHA256: strings.Repeat("a", 64), Label: "start", ReceivedAt: received}, Days: []marketdata.Day{{Date: date, Issues: []string{"coverage_unverified"}, Bars: bars}}}
}

func newFeed(t *testing.T) *Feed {
	t.Helper()
	f, err := New(Config{Date: date, Symbol: "7203@1", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSuccessiveResponsesWaitCloseAndNeverSumOverlap(t *testing.T) {
	f := newFeed(t)
	start := marketdata.Clock(date, 9, 0)
	// In-progress changes are allowed before that minute becomes a candidate.
	d := fixture(start.Add(30*time.Second), 1)
	if out := f.Offer(d.Source.ReceivedAt, d); !out.Accepted || len(out.Candidates) != 0 {
		t.Fatal(out)
	}
	d = fixture(start.Add(time.Minute+time.Second), 2)
	d.Days[0].Bars[0].Close = 101
	if out := f.Offer(d.Source.ReceivedAt, d); !out.Accepted || len(out.Candidates) != 0 {
		t.Fatal("early candidate", out)
	}
	d.Source.ReceivedAt = start.Add(time.Minute + CloseLag)
	out := f.Offer(d.Source.ReceivedAt, d)
	if !out.Accepted || len(out.Candidates) != 1 || out.Candidates[0].Late || out.Candidates[0].StrategyEligible || *out.Candidates[0].Bar.Volume != 100 || len(out.Candidates[0].Issues) != 2 {
		t.Fatal("candidate provenance or quantity", out)
	}
	// Returned data and later caller mutations must not change the stored key.
	*out.Candidates[0].Bar.Volume = 999
	out.Candidates[0].Issues[0] = "changed"
	d.Source.ReceivedAt = start.Add(2*time.Minute + CloseLag)
	d.Days[0].Bars[0].FirstRow = 500
	d.Days[0].Bars[0].LastRow = 700
	out = f.Offer(d.Source.ReceivedAt, d)
	if !out.Accepted || out.Duplicates != 1 || len(out.Candidates) != 1 || *out.Candidates[0].Bar.Volume != 100 {
		t.Fatal("overlap summed or metadata treated as revision", out)
	}
	if repeated := f.Offer(d.Source.ReceivedAt, d); !repeated.Accepted || repeated.Duplicates != 2 || len(repeated.Candidates) != 0 {
		t.Fatal("repeat generated candidates", repeated)
	}
	if state := f.Snapshot(); state.Candidates != 2 || state.LiveReady || state.Late != 0 {
		t.Fatal(state)
	}
}

func TestRevisionsGapsAndContractFaultsStopAtomically(t *testing.T) {
	for _, kind := range []string{"price", "volume", "quality", "old_missing", "gap", "future_received", "clock_reversal", "received_reversal", "identity", "environment", "origin", "format", "converter", "reused_time", "future_market_time"} {
		t.Run(kind, func(t *testing.T) {
			f := newFeed(t)
			at := marketdata.Clock(date, 9, 1).Add(CloseLag)
			d := fixture(at, 1)
			if !f.Offer(at, d).Accepted {
				t.Fatal("bootstrap")
			}
			d = fixture(at.Add(2*time.Minute), 3)
			now := d.Source.ReceivedAt
			want := "candidate_minute_revised"
			switch kind {
			case "price":
				d.Days[0].Bars[0].Close++
			case "volume":
				v := int64(101)
				d.Days[0].Bars[0].Volume = &v
			case "quality":
				d.Days[0].Issues = append(d.Days[0].Issues, "new_uncertainty")
			case "old_missing":
				d.Days[0].Bars = d.Days[0].Bars[1:]
				want = "previous_candidate_missing"
			case "gap":
				d.Days[0].Bars = d.Days[0].Bars[:2] // Last due minute missing: no partial frame commit.
				want = "closed_minute_missing"
			case "future_received":
				now = now.Add(-time.Second)
				want = "history_received_time_invalid"
			case "clock_reversal":
				now = at.Add(-time.Second)
				want = "observation_clock_reversed_or_invalid"
			case "received_reversal":
				d.Source.ReceivedAt = at.Add(-time.Second)
				want = "history_received_time_invalid"
			case "identity":
				d.Symbol = "8306@1"
				want = "history_identity_or_contract_invalid"
			case "environment":
				d.Environment = "test"
				want = "history_identity_or_contract_invalid"
			case "origin":
				d.Origin = "synthetic"
				want = "history_identity_or_contract_invalid"
			case "format":
				d.Source.Format = "chart-csv"
				want = "history_identity_or_contract_invalid"
			case "converter":
				d.Converter = "new-version"
				want = "history_identity_or_contract_invalid"
			case "reused_time":
				d.Source.ReceivedAt = at
				d.Source.SHA256 = strings.Repeat("b", 64)
				want = "history_time_reused_with_changed_source"
			case "future_market_time":
				d.Days[0].Bars[2].LastTime = now.Add(time.Second)
				want = "history_market_time_in_future"
			}
			out := f.Offer(now, d)
			if out.Accepted || out.Reason != want || len(out.Candidates) != 0 || f.Snapshot().Candidates != 1 || f.Snapshot().Frames != 1 {
				t.Fatalf("wanted=%s out=%+v state=%+v", want, out, f.Snapshot())
			}
			if again := f.Offer(at.Add(5*time.Minute), fixture(at.Add(5*time.Minute), 6)); again.Accepted || again.Reason != want {
				t.Fatal("halt automatically resumed", again)
			}
		})
	}
}

func TestLateArchiveDoesNotBecomeFreshAndWindowIsBounded(t *testing.T) {
	f := newFeed(t)
	at := marketdata.Clock(date, 16, 0)
	d := fixture(at, 111)
	out := f.Offer(at, d)
	if !out.Accepted || len(out.Candidates) != 111 || !f.Snapshot().Complete || f.Snapshot().Late != 111 || f.Snapshot().LiveReady {
		t.Fatal("late archive status", f.Snapshot())
	}
	for _, c := range out.Candidates {
		if !c.Late || c.StrategyEligible || !c.ObservedAt.Equal(at) || !c.ReceivedAt.Equal(at) {
			t.Fatal("backdated to bar close")
		}
	}
	if second := f.Offer(at.Add(time.Minute), fixture(at.Add(time.Minute), 112)); !second.Accepted || second.Duplicates != 111 || len(second.Candidates) != 0 {
		t.Fatal("window grew", second)
	}
}

func TestEarliestCandidateFaultHasDeterministicPriority(t *testing.T) {
	f := newFeed(t)
	at := marketdata.Clock(date, 9, 2).Add(CloseLag)
	d := fixture(at, 2)
	if !f.Offer(at, d).Accepted {
		t.Fatal("bootstrap")
	}
	d = fixture(at.Add(time.Minute), 3)
	d.Days[0].Bars[0].Close++
	d.Days[0].Bars = append(d.Days[0].Bars[:1], d.Days[0].Bars[2:]...)
	if out := f.Offer(at.Add(time.Minute), d); out.Reason != "candidate_minute_revised" || f.Snapshot().Candidates != 2 {
		t.Fatal("later missing candidate masked the earlier revision", out)
	}
}

func TestLatencyIncludesObservationAndDeadlineBoundary(t *testing.T) {
	for _, delay := range []time.Duration{ArrivalDeadline, ArrivalDeadline + time.Nanosecond} {
		t.Run(fmt.Sprint(delay), func(t *testing.T) {
			f := newFeed(t)
			end := marketdata.Clock(date, 9, 1)
			d := fixture(end.Add(CloseLag), 1)
			out := f.Offer(end.Add(delay), d)
			if !out.Accepted || len(out.Candidates) != 1 || out.Candidates[0].Late != (delay > ArrivalDeadline) {
				t.Fatal("observation latency ignored", out)
			}
		})
	}
}

func TestQuantityUnknownAndUnverifiedIssuesNeverPromoted(t *testing.T) {
	f := newFeed(t)
	at := marketdata.Clock(date, 9, 1).Add(CloseLag)
	d := fixture(at, 1)
	d.Days[0].Bars[0].Volume = nil
	d.Days[0].Bars[0].Issues = []string{"quantity_unknown"}
	out := f.Offer(at, d)
	if !out.Accepted || out.Candidates[0].Bar.Volume != nil || len(out.Candidates[0].Issues) != 3 || out.Candidates[0].StrategyEligible {
		t.Fatal("unknown changed or cleared", out)
	}
	for _, config := range []Config{{date, "", "production"}, {"bad-date", "7203@1", "production"}, {date, "7203@1", "live"}} {
		if _, err := New(config); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
