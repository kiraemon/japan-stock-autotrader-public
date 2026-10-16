package marketdata

import (
	"strings"
	"testing"
	"time"
)

const boardFixture = `{"Symbol":"0000","Exchange":1,"BidPrice":101,"AskPrice":99,"Sell1":{"Price":101,"Qty":40,"Time":"2026-10-16T09:16:03+09:00","Sign":"0101"},"Buy1":{"Price":99,"Qty":60,"Time":"2026-10-16T09:16:03+09:00","Sign":"0101"}}`

func TestKabuQuoteUsesDocumentedBuyAndSellNotReversedTopLevelNames(t *testing.T) {
	at := Clock("2026-10-16", 9, 16).Add(3500 * time.Millisecond)
	q, err := KabuBoardQuote([]byte(boardFixture), "0000@1", "test", at)
	if err != nil || len(q.Issues) != 0 || q.Bid.Price != 99*PriceScale || q.Ask.Price != 101*PriceScale || *q.Ask.Quantity != 40 || *q.Bid.Quantity != 60 || q.Origin != "observed" || len(q.SourceSHA256) != 64 {
		t.Fatalf("normalisation failed: %v", err)
	}
}

func TestKabuQuoteUnknownAndAbnormalFieldsStayIssues(t *testing.T) {
	at := Clock("2026-10-16", 9, 16).Add(3500 * time.Millisecond)
	for _, c := range []struct{ from, to string }{
		{`"Qty":40`, `"Qty":null`},
		{`"Price":101`, `"Price":0`},
		{`"Sign":"0101"`, `"Sign":"0102"`},
		{`09:16:03`, `09:16:04`},
		{`"Price":99`, `"Price":101`},
	} {
		q, err := KabuBoardQuote([]byte(strings.ReplaceAll(boardFixture, c.from, c.to)), "0000@1", "test", at)
		if err != nil || len(q.Issues) == 0 {
			t.Fatalf("unknown quality discarded: %v", err)
		}
	}
	if _, err := KabuBoardQuote([]byte(boardFixture), "7203@1", "test", at); err == nil {
		t.Fatal("identity mismatch accepted")
	}
	q, err := KabuBoardQuote([]byte(`{"Symbol":"0000","Exchange":"1"}`), "0000@1", "test", at)
	if err != nil || len(q.Issues) != 2 || q.Ask.Quantity != nil {
		t.Fatal("missing side became known zero")
	}
}
