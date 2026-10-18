package buildbars

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

// FromJQuantsCLI converts the bounded capture archive, never a kabu CSV alias.
// The original archive preserves Va; this OHLCV contract does not yet use it.
func FromJQuantsCLI(ctx context.Context, raw []byte, symbol string) (marketdata.Dataset, error) {
	var archive struct {
		Version  string          `json:"version"`
		Provider string          `json:"provider"`
		CLI      string          `json:"cli_version"`
		Stats    json.RawMessage `json:"request_stats"`
		Complete bool            `json:"complete"`
		Results  []struct {
			Code     string    `json:"code"`
			From     string    `json:"from"`
			To       string    `json:"to"`
			Started  time.Time `json:"started_at"`
			Captured time.Time `json:"captured_at"`
			Data     []struct {
				Date     string      `json:"Date"`
				Time     string      `json:"Time"`
				Code     string      `json:"Code"`
				Open     json.Number `json:"O"`
				High     json.Number `json:"H"`
				Low      json.Number `json:"L"`
				Close    json.Number `json:"C"`
				Volume   json.Number `json:"Vo"`
				Turnover json.Number `json:"Va"`
			} `json:"data"`
		} `json:"results"`
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if len(raw) > 64<<20 || decode.Decode(&archive) != nil || decode.Decode(new(any)) != io.EOF || archive.Version != "jquants-cli-capture-v0.1" || archive.Provider != "jquants" || archive.CLI != "1.6.0" || !archive.Complete || !strings.HasSuffix(symbol, "@1") {
		return marketdata.Dataset{}, errors.New("完了したJ-Quants取得アーカイブの形式を確認してください")
	}
	var resultIndex = -1
	for i, result := range archive.Results {
		if result.Code+"@1" == symbol {
			if resultIndex >= 0 {
				return marketdata.Dataset{}, errors.New("J-Quantsの取得銘柄が重複しています")
			}
			resultIndex = i
		}
	}
	if resultIndex < 0 {
		return marketdata.Dataset{}, errors.New("J-Quants取得結果に指定銘柄がありません")
	}
	result := archive.Results[resultIndex]
	from, fromErr := time.Parse("2006-01-02", result.From)
	to, toErr := time.Parse("2006-01-02", result.To)
	if result.Started.IsZero() || result.Captured.Before(result.Started) || len(result.Data) == 0 || fromErr != nil || toErr != nil || from.Format("2006-01-02") != result.From || to.Format("2006-01-02") != result.To || to.Before(from) || result.From < "2024-11-05" {
		return marketdata.Dataset{}, errors.New("J-Quantsの取得時刻・件数・現行立会時間の対応期間を確認してください")
	}
	d := dataset(raw, symbol, "production", "jquants-cli-minute-v1.6.0", result.Captured)
	d.Converter = Version + "/jquants-v0.1"
	days := map[string]*marketdata.Day{}
	seen := map[time.Time]bool{}
	for index, row := range result.Data {
		if ctx.Err() != nil {
			return d, errors.New("J-Quants分足変換を中断しました")
		}
		clock := row.Time
		if len(clock) == 5 {
			clock += ":00"
		}
		at, err := time.ParseInLocation("2006-01-02 15:04:05", row.Date+" "+clock, marketdata.JST)
		if err != nil || !at.Equal(at.Truncate(time.Minute)) || at.After(result.Captured) || row.Date < result.From || row.Date > result.To || (row.Code != result.Code && row.Code != result.Code+"0") || seen[at] {
			return d, errors.New("J-Quantsの銘柄・日時・範囲・重複を確認してください")
		}
		seen[at] = true
		b := marketdata.Bar{Start: at, FirstRow: index + 1, LastRow: index + 1, Issues: []string{}}
		for _, p := range []struct {
			value  json.Number
			target *int64
		}{{row.Open, &b.Open}, {row.High, &b.High}, {row.Low, &b.Low}, {row.Close, &b.Close}} {
			value, err := marketdata.ParsePrice(string(p.value))
			if err != nil {
				return d, err
			}
			*p.target = value
		}
		if !marketdata.ValidPrices(b) {
			return d, errors.New("J-QuantsのOHLCが整合しません")
		}
		b.Volume, err = marketdata.ParseVolume(string(row.Volume))
		if err != nil {
			return d, err
		}
		if days[row.Date] == nil {
			days[row.Date] = &marketdata.Day{Date: row.Date, Issues: []string{"coverage_unverified", "trading_status_unverified", "source_crosscheck_unverified", "corporate_actions_unverified"}, Bars: []marketdata.Bar{}, Events: []marketdata.Event{}}
		}
		if !marketdata.RegularMinute(at) {
			if row.Time != "11:30" && row.Time != "11:30:00" && row.Time != "15:30" && row.Time != "15:30:00" {
				return d, errors.New("J-Quantsの立会時間外の分足を確認してください")
			}
			if b.Open != b.Close || b.High != b.Close || b.Low != b.Close {
				return d, errors.New("J-Quantsの板寄せ境界足を確認してください")
			}
			days[row.Date].Events = append(days[row.Date].Events, marketdata.Event{Time: at, Price: b.Close, Volume: b.Volume, Row: index + 1})
			continue
		}
		if at.Add(time.Minute).After(result.Captured) {
			b.Issues = append(b.Issues, "unfinished_minute")
		}
		days[row.Date].Bars = append(days[row.Date].Bars, b)
	}
	for _, day := range days {
		sort.Slice(day.Bars, func(i, j int) bool { return day.Bars[i].Start.Before(day.Bars[j].Start) })
		sort.Slice(day.Events, func(i, j int) bool { return day.Events[i].Time.Before(day.Events[j].Time) })
		d.Days = append(d.Days, *day)
	}
	sort.Slice(d.Days, func(i, j int) bool { return d.Days[i].Date < d.Days[j].Date })
	return d, marketdata.Validate(d)
}
