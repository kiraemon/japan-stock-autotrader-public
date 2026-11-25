// Package buildbars converts one immutable source into quality-tagged minutes.
// Unknown source semantics stay unknown; no real-data quality override exists.
package buildbars

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

const Version = "buildbars-v0.2"

func sourceHash(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func dataset(raw []byte, symbol, environment, format string, received time.Time) marketdata.Dataset {
	return marketdata.Dataset{Version: 1, PriceScale: marketdata.PriceScale, Symbol: symbol, Environment: environment, Origin: "observed", Converter: Version, Source: marketdata.Source{Format: format, SHA256: sourceHash(raw), Label: "start", ReceivedAt: received}, Days: []marketdata.Day{}}
}
func dayIssues() []string {
	return []string{"coverage_unverified", "quantity_semantics_unverified", "trading_status_unverified", "source_crosscheck_unverified"}
}

type edge struct {
	first, last                   time.Time
	firstPrice, lastPrice         int64
	openSensitive, closeSensitive bool
}

func (e *edge) add(t time.Time, p int64) {
	if e.first.IsZero() || t.Before(e.first) {
		e.first = t
		e.firstPrice = p
		e.openSensitive = false
	} else if t.Equal(e.first) && p != e.firstPrice {
		e.openSensitive = true
	}
	if e.last.IsZero() || t.After(e.last) {
		e.last = t
		e.lastPrice = p
		e.closeSensitive = false
	} else if t.Equal(e.last) && p != e.lastPrice {
		e.closeSensitive = true
	}
}

func FromHistory(ctx context.Context, raw []byte) (marketdata.Dataset, error) {
	var a struct {
		Version     int        `json:"schema_version"`
		Environment string     `json:"environment"`
		Symbol      string     `json:"symbol"`
		Complete    bool       `json:"complete"`
		Started     time.Time  `json:"started_at"`
		Received    *time.Time `json:"history_received_at"`
		Finished    time.Time  `json:"finished_at"`
		Trades      struct {
			Symbol   string
			Exchange json.RawMessage
			Count    *int `json:"TradingPriceCount"`
			Rows     *[]struct {
				Time          time.Time
				Price, Volume json.Number
			} `json:"TradingPrice"`
		} `json:"time_and_sales"`
	}
	if len(raw) > 64<<20 || json.Unmarshal(raw, &a) != nil || a.Version != 1 || !a.Complete || a.Received == nil || a.Started.IsZero() || a.Started.After(*a.Received) || a.Received.After(a.Finished) || a.Trades.Rows == nil || a.Trades.Count == nil || *a.Trades.Count != len(*a.Trades.Rows) {
		return marketdata.Dataset{}, errors.New("完了した履歴アーカイブの形式・時刻・件数を確認してください")
	}
	var exchange string
	if json.Unmarshal(a.Trades.Exchange, &exchange) != nil {
		exchange = string(a.Trades.Exchange)
	}
	if a.Symbol != a.Trades.Symbol+"@"+exchange {
		return marketdata.Dataset{}, errors.New("履歴の銘柄・市場が一致しません")
	}
	d := dataset(raw, a.Symbol, a.Environment, "kabu-timeandsales", *a.Received)
	days := map[string]*marketdata.Day{}
	bars := map[time.Time]*marketdata.Bar{}
	edges := map[time.Time]*edge{}
	var previous time.Time
	for index, row := range *a.Trades.Rows {
		if ctx.Err() != nil {
			return d, errors.New("分足変換を中断しました")
		}
		p, err := marketdata.ParsePrice(string(row.Price))
		if err != nil {
			return d, err
		}
		v, err := marketdata.ParseVolume(string(row.Volume))
		if err != nil {
			return d, err
		}
		if row.Time.IsZero() || row.Time.After(a.Received.Add(2*time.Second)) || (!previous.IsZero() && row.Time.Before(previous)) {
			return d, errors.New("歩み値の不正時刻・逆行を検出しました（並べ替えは行いません）")
		}
		previous = row.Time
		day := marketdata.Date(row.Time)
		if days[day] == nil {
			days[day] = &marketdata.Day{Date: day, Issues: dayIssues(), Bars: []marketdata.Bar{}, Events: []marketdata.Event{}}
		}
		if !marketdata.RegularMinute(row.Time) {
			days[day].Events = append(days[day].Events, marketdata.Event{Time: row.Time, Price: p, Volume: v, Row: index + 1})
			continue
		}
		minute := row.Time.UTC().Truncate(time.Minute)
		b := bars[minute]
		if b == nil {
			zero := int64(0)
			b = &marketdata.Bar{Start: minute, Open: p, High: p, Low: p, Close: p, Volume: &zero, FirstRow: index + 1, FirstTime: row.Time, Issues: []string{}}
			bars[minute] = b
			edges[minute] = &edge{}
		}
		if *b.Volume > math.MaxInt64-*v {
			return d, errors.New("分足出来高の上限を超えました")
		}
		*b.Volume += *v
		if p > b.High {
			b.High = p
		}
		if p < b.Low {
			b.Low = p
		}
		b.Close = p
		b.LastRow = index + 1
		b.LastTime = row.Time
		edges[minute].add(row.Time, p)
	}
	for minute, b := range bars {
		e := edges[minute]
		if e.openSensitive {
			b.Issues = append(b.Issues, "open_order_unverified")
		}
		if e.closeSensitive {
			b.Issues = append(b.Issues, "close_order_unverified")
		}
		if minute.Add(time.Minute).After(*a.Received) {
			b.Issues = append(b.Issues, "unfinished_minute")
		}
		days[marketdata.Date(minute)].Bars = append(days[marketdata.Date(minute)].Bars, *b)
	}
	for _, day := range days {
		sort.Slice(day.Bars, func(i, j int) bool { return day.Bars[i].Start.Before(day.Bars[j].Start) })
		d.Days = append(d.Days, *day)
	}
	sort.Slice(d.Days, func(i, j int) bool { return d.Days[i].Date < d.Days[j].Date })
	return d, marketdata.Validate(d)
}

// FromCSV accepts an explicit minute-label convention, never guesses it.
// The native six-column export has a combined date/time and a known CP932
// header. Only that exact legacy header is mapped; numeric rows remain ASCII.
func FromCSV(ctx context.Context, raw []byte, symbol, environment, label string, received time.Time) (marketdata.Dataset, error) {
	d := dataset(raw, symbol, environment, "chart-csv", received)
	d.Source.Label = label
	if len(raw) > 64<<20 || (label != "start" && label != "end") || received.IsZero() {
		return d, errors.New("CSVのサイズ、開始/終了ラベルと取得日時を確認してください")
	}
	input, format, err := csvInput(raw)
	if err != nil {
		return d, err
	}
	d.Source.Format = format
	r := csv.NewReader(bytes.NewReader(input))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return d, errors.New("CSVの見出しを読み取れません")
	}
	native := strings.Join(header, ",") == "日付,始値,高値,安値,終値,売買高"
	if native {
		header = []string{"datetime", "open", "high", "low", "close", "volume"}
		if format == "chart-csv" {
			d.Source.Format = "kabu-chart-csv-utf8"
		}
	}
	aliases := map[string]string{"日付": "date", "時刻": "time", "始値": "open", "高値": "high", "安値": "low", "終値": "close", "出来高": "volume"}
	columns := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		if alias, ok := aliases[h]; ok {
			h = alias
		}
		switch h {
		case "datetime", "date", "time", "open", "high", "low", "close", "volume":
			if _, exists := columns[h]; exists {
				return d, errors.New("CSVの必要列が重複しています")
			}
			columns[h] = i
		}
	}
	required := []string{"date", "time", "open", "high", "low", "close", "volume"}
	if native {
		required = []string{"datetime", "open", "high", "low", "close", "volume"}
	} else if _, ambiguous := columns["datetime"]; ambiguous {
		return d, errors.New("日時結合列はkabuの6列形式だけに対応しています")
	}
	for _, h := range required {
		if _, ok := columns[h]; !ok {
			return d, errors.New("CSVの必要列が見つかりません（date,time,open,high,low,close,volume）")
		}
	}
	dayMap := map[string]*marketdata.Day{}
	var previous time.Time
	rowNumber := 1
	for {
		if ctx.Err() != nil {
			return d, errors.New("CSV変換を中断しました")
		}
		fields, err := r.Read()
		if err == io.EOF {
			break
		}
		rowNumber++
		if err != nil || len(fields) != len(header) || rowNumber > 1_000_001 {
			return d, errors.New("CSVの行形式・件数を確認してください")
		}
		field := func(name string) string { return strings.TrimSpace(fields[columns[name]]) }
		var datetime string
		if native {
			datetime = strings.ReplaceAll(field("datetime"), "/", "-")
		} else {
			clock := field("time")
			if strings.Count(clock, ":") == 1 {
				clock += ":00"
			}
			datetime = strings.ReplaceAll(field("date"), "/", "-") + " " + clock
		}
		stamp, err := time.ParseInLocation("2006-01-02 15:04:05", datetime, marketdata.JST)
		if err != nil || !stamp.Equal(stamp.Truncate(time.Minute)) || stamp.After(received) {
			return d, errors.New("CSVの日時を確認してください")
		}
		start := stamp
		if label == "end" {
			start = start.Add(-time.Minute)
		}
		if !previous.IsZero() && !start.After(previous) {
			return d, errors.New("CSVの重複・時刻逆行を検出しました（並べ替えは行いません）")
		}
		previous = start
		b := marketdata.Bar{Start: start, Issues: []string{}, FirstRow: rowNumber, LastRow: rowNumber}
		for _, item := range []struct {
			name   string
			target *int64
		}{{"open", &b.Open}, {"high", &b.High}, {"low", &b.Low}, {"close", &b.Close}} {
			p, err := marketdata.ParsePrice(field(item.name))
			if err != nil {
				return d, err
			}
			*item.target = p
		}
		v, err := marketdata.ParseVolume(field("volume"))
		if err != nil {
			return d, err
		}
		b.Volume = v
		if !marketdata.ValidPrices(b) {
			return d, errors.New("CSVの高安と始値・終値が整合しません")
		}
		day := marketdata.Date(start)
		if dayMap[day] == nil {
			dayMap[day] = &marketdata.Day{Date: day, Issues: append(dayIssues(), "csv_semantics_unverified"), Bars: []marketdata.Bar{}, Events: []marketdata.Event{}}
		}
		if !marketdata.RegularMinute(start) {
			dayMap[day].Events = append(dayMap[day].Events, marketdata.Event{Time: stamp, Price: b.Close, Volume: v, Row: rowNumber})
			continue
		}
		if start.Add(time.Minute).After(received) {
			b.Issues = append(b.Issues, "unfinished_minute")
		}
		dayMap[day].Bars = append(dayMap[day].Bars, b)
	}
	for _, day := range dayMap {
		d.Days = append(d.Days, *day)
	}
	sort.Slice(d.Days, func(i, j int) bool { return d.Days[i].Date < d.Days[j].Date })
	return d, marketdata.Validate(d)
}

// This is not a general CP932 decoder: unknown headers, encodings and
// non-ASCII legacy data rows are refused instead of replacing invalid bytes.
const nativeCP932Header = "\x93\xfa\x95\x74,\x8e\x6e\x92\x6c,\x8d\x82\x92\x6c,\x88\xc0\x92\x6c,\x8f\x49\x92\x6c,\x94\x84\x94\x83\x8d\x82"

func csvInput(raw []byte) ([]byte, string, error) {
	if utf8.Valid(raw) {
		return bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), "chart-csv", nil
	}
	header, rows, ok := bytes.Cut(raw, []byte{'\n'})
	if !ok || string(bytes.TrimSuffix(header, []byte{'\r'})) != nativeCP932Header {
		return nil, "", errors.New("CSVの文字コード・見出しは未対応です（UTF-8または確認済みkabuのCP932形式）")
	}
	for _, b := range rows {
		if b >= 0x80 {
			return nil, "", errors.New("kabuのCP932形式のデータ行に未対応の文字があります")
		}
	}
	return append([]byte("日付,始値,高値,安値,終値,売買高\n"), rows...), "kabu-chart-csv-cp932", nil
}
