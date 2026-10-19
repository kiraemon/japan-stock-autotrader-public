package observespread

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

func fixture() Input {
	at := marketdata.Clock("2026-10-16", 9, 16).Add(3 * time.Second)
	quantity := int64(100)
	return Input{Quote: marketdata.Quote{Version: marketdata.QuoteVersion, Symbol: "0000@1", Environment: "production", Origin: "observed", ReceivedAt: at.Add(500 * time.Millisecond), SourceSHA256: strings.Repeat("a", 64), Bid: marketdata.QuoteSide{Price: 99 * marketdata.PriceScale, Quantity: &quantity, Time: at, Sign: "0101"}, Ask: marketdata.QuoteSide{Price: 101 * marketdata.PriceScale, Quantity: &quantity, Time: at, Sign: "0101"}}, SnapshotSHA: strings.Repeat("b", 64)}
}

func TestSimultaneousIdenticalBoardsSortBySnapshotSource(t *testing.T) {
	a, b := fixture(), fixture()
	b.SnapshotSHA = strings.Repeat("c", 64)
	inspection := marketdata.Clock("2026-10-19", 15, 0)
	first, err := Analyze(context.Background(), []Input{a, b}, inspection)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Analyze(context.Background(), []Input{b, a}, inspection)
	if err != nil || !reflect.DeepEqual(first, second) || first.Observations[0].Duplicate || !first.Observations[1].Duplicate {
		t.Fatal("simultaneous identical board provenance or duplicate identity changed")
	}
}

func TestReceiptStatisticsKeepDuplicatesAndUniqueUpdatesSeparate(t *testing.T) {
	a := fixture()
	b := fixture()
	b.Quote.ReceivedAt = b.Quote.ReceivedAt.Add(100 * time.Millisecond)
	b.Quote.SourceSHA256 = strings.Repeat("c", 64)
	c := fixture()
	c.Quote.Bid.Price = 98 * marketdata.PriceScale
	c.Quote.Ask.Price = 102 * marketdata.PriceScale
	c.Quote.ReceivedAt = c.Quote.ReceivedAt.Add(time.Second)
	c.Quote.Bid.Time = c.Quote.Bid.Time.Add(time.Second)
	c.Quote.Ask.Time = c.Quote.Ask.Time.Add(time.Second)
	d := fixture()
	d.Quote.ReceivedAt = d.Quote.ReceivedAt.Add(4 * time.Second)
	input := []Input{d, c, b, a}
	before, _ := json.Marshal(input)
	inspection := marketdata.Clock("2026-10-19", 15, 0)
	r, err := Analyze(context.Background(), input, inspection)
	if err != nil || r.Samples != 4 || r.StructurallyValid != 4 || r.Fresh2 != 3 || len(r.Groups) != 1 || r.ForwardTest || r.ExecutionIncluded || r.Trading || r.Quality {
		t.Fatal("observation scope or count", err)
	}
	g := r.Groups[0]
	if g.Fresh5 != 4 || g.Fresh30 != 4 || g.DuplicateBBO != 2 || g.UniqueFresh2 != 2 || g.Yen.Median != "2" || g.BPS.Median != "200" || g.BPS.P90 != "400" || g.UniqueBPS.Median != "300" || g.BPS.Samples != 3 || g.UniqueBPS.Samples != 2 {
		t.Fatal("sample weighting or exact distribution changed")
	}
	if !r.Observations[1].Duplicate || !r.Observations[1].Fresh2 || r.Observations[3].Fresh2 || len(r.Observations[3].Issues) != 1 || r.Observations[0].BuyTime.IsZero() {
		t.Fatal("duplicate, timestamps or stale evidence lost")
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	again, err := Analyze(context.Background(), []Input{a, b, c, d}, inspection)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("input order changed result")
	}
}

func TestUnsupportedAndForgedQuotesDoNotProduceSpreadStatistics(t *testing.T) {
	inspection := marketdata.Clock("2026-10-19", 15, 0)
	for _, fault := range []string{"partial_record", "synthetic", "environment", "hash", "quantity", "crossed", "sign", "future_side", "future_receipt", "prior_day", "lunch", "closing_auction", "weekend", "quality"} {
		t.Run(fault, func(t *testing.T) {
			x := fixture()
			switch fault {
			case "partial_record":
				x.RecordIssue = true
			case "synthetic":
				x.Quote.Origin = "synthetic"
			case "environment":
				x.Quote.Environment = "test"
			case "hash":
				x.Quote.SourceSHA256 = "unknown"
			case "quantity":
				x.Quote.Ask.Quantity = nil
			case "crossed":
				x.Quote.Ask.Price = x.Quote.Bid.Price
			case "sign":
				x.Quote.Ask.Sign = "0102"
			case "future_side":
				x.Quote.Bid.Time = x.Quote.ReceivedAt.Add(time.Second)
			case "future_receipt":
				x.Quote.ReceivedAt = inspection.Add(time.Second)
			case "prior_day":
				x.Quote.Bid.Time = x.Quote.Bid.Time.Add(-24 * time.Hour)
			case "lunch":
				x.Quote.ReceivedAt = marketdata.Clock("2026-10-16", 12, 0)
			case "closing_auction":
				x.Quote.ReceivedAt = marketdata.Clock("2026-10-16", 15, 25)
			case "weekend":
				x.Quote.ReceivedAt = marketdata.Clock("2026-10-17", 9, 16)
			case "quality":
				x.Quote.Issues = []string{"private-marker"}
			}
			r, err := Analyze(context.Background(), []Input{x}, inspection)
			if err != nil || r.StructurallyValid != 0 || r.Fresh2 != 0 || len(r.Observations[0].Issues) == 0 || r.Groups[0].BPS != nil || r.Observations[0].BPS != "" {
				t.Fatal("bad quote promoted", err)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "private-marker") {
				t.Fatal("untrusted issue text escaped")
			}
		})
	}
}

func TestFixedFreshnessBoundariesAndGrouping(t *testing.T) {
	for _, age := range []time.Duration{2 * time.Second, 2*time.Second + 1, 5 * time.Second, 5*time.Second + 1, 30 * time.Second, 30*time.Second + 1} {
		x := fixture()
		x.Quote.ReceivedAt = x.Quote.Bid.Time.Add(age)
		r, err := Analyze(context.Background(), []Input{x}, marketdata.Clock("2026-10-19", 15, 0))
		if err != nil {
			t.Fatal(err)
		}
		g := r.Groups[0]
		if (g.Fresh2 == 1) != (age <= 2*time.Second) || (g.Fresh5 == 1) != (age <= 5*time.Second) || (g.Fresh30 == 1) != (age <= 30*time.Second) {
			t.Fatal("inclusive freshness boundary changed")
		}
	}
	a := fixture()
	b := fixture()
	b.Quote.Symbol = "0001@1"
	c := fixture()
	c.Quote.ReceivedAt = marketdata.Clock("2026-10-16", 10, 1)
	c.Quote.Bid.Time, c.Quote.Ask.Time = c.Quote.ReceivedAt, c.Quote.ReceivedAt
	r, err := Analyze(context.Background(), []Input{c, b, a}, marketdata.Clock("2026-10-19", 15, 0))
	if err != nil || len(r.Groups) != 3 {
		t.Fatal("symbol/time group conflated")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, []Input{a}, time.Now()); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := Analyze(context.Background(), nil, time.Now()); err == nil {
		t.Fatal("empty observations accepted")
	}
	a.SnapshotSHA = "invalid"
	if _, err := Analyze(context.Background(), []Input{a}, time.Now()); err == nil {
		t.Fatal("unbound source accepted")
	}
}
