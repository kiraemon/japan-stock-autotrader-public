package marketdata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const QuoteVersion = "kabu-level-one-quote-v0.1"

type QuoteSide struct {
	Price    int64     `json:"price_scaled"`
	Quantity *int64    `json:"quantity_shares"`
	Time     time.Time `json:"market_time"`
	Sign     string    `json:"sign"`
}

type Quote struct {
	Version      string    `json:"version"`
	Symbol       string    `json:"symbol"`
	Environment  string    `json:"environment"`
	Origin       string    `json:"origin"`
	ReceivedAt   time.Time `json:"received_at"`
	SourceSHA256 string    `json:"source_sha256"`
	Bid          QuoteSide `json:"best_buy"`
	Ask          QuoteSide `json:"best_sell"`
	Issues       []string  `json:"issues"`
}

// KabuBoardQuote deliberately reads Buy1/Sell1. The broker documents that its
// top-level Bid* means SELL and Ask* means BUY. Never infer sides by those names.
// Normalisation describes missing/abnormal values; it cannot certify liquidity.
func KabuBoardQuote(raw []byte, symbol, environment string, received time.Time) (Quote, error) {
	q := Quote{Version: QuoteVersion, Symbol: symbol, Environment: environment, Origin: "observed", ReceivedAt: received, Issues: []string{}}
	if len(raw) > 8<<20 || !symbolPattern.MatchString(symbol) || (environment != "test" && environment != "production") || received.IsZero() {
		return q, errors.New("板データの入力・銘柄・環境・受信時刻を確認してください")
	}
	type level struct {
		Price, Qty json.Number
		Time       time.Time
		Sign       string
	}
	var b struct {
		Symbol      string
		Exchange    json.RawMessage
		Buy1, Sell1 *level
	}
	if json.Unmarshal(raw, &b) != nil {
		return q, errors.New("板データの形式を確認してください")
	}
	exchange := string(b.Exchange)
	var quoted string
	if json.Unmarshal(b.Exchange, &quoted) == nil {
		exchange = quoted
	}
	if b.Symbol+"@"+exchange != symbol {
		return q, errors.New("板データの銘柄・市場が一致しません")
	}
	h := sha256.Sum256(raw)
	q.SourceSHA256 = hex.EncodeToString(h[:])
	decode := func(value *level, name string) QuoteSide {
		s := QuoteSide{}
		if value == nil {
			q.Issues = append(q.Issues, name+"_missing")
			return s
		}
		var err error
		s.Price, err = ParsePrice(string(value.Price))
		if err != nil {
			q.Issues = append(q.Issues, name+"_price_unavailable")
		}
		s.Quantity, err = ParseVolume(string(value.Qty))
		if err != nil || s.Quantity != nil && *s.Quantity == 0 {
			q.Issues = append(q.Issues, name+"_quantity_unavailable")
		}
		s.Time, s.Sign = value.Time, value.Sign
		if s.Time.IsZero() || s.Time.After(received) {
			q.Issues = append(q.Issues, name+"_time_unavailable")
		}
		if s.Sign != "0101" {
			q.Issues = append(q.Issues, name+"_not_general_quote")
		}
		return s
	}
	q.Bid, q.Ask = decode(b.Buy1, "buy"), decode(b.Sell1, "sell")
	if q.Bid.Price > 0 && q.Ask.Price > 0 && q.Bid.Price >= q.Ask.Price {
		q.Issues = append(q.Issues, "locked_or_crossed_book")
	}
	return q, nil
}
