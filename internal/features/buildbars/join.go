package buildbars

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader-public/internal/marketdata"
)

// JoinJQuants joins validated monthly inputs without filling absent days or
// changing any day-quality issue. Its source hash identifies the sorted original
// source references, rather than pretending to be one original gzip's hash.
func JoinJQuants(ctx context.Context, inputs []marketdata.Dataset) (marketdata.Dataset, error) {
	return joinJQuants(ctx, inputs, false)
}

func JoinJQuantsWithTurnover(ctx context.Context, inputs []marketdata.Dataset) (marketdata.Dataset, error) {
	return joinJQuants(ctx, inputs, true)
}

func joinJQuants(ctx context.Context, inputs []marketdata.Dataset, turnover bool) (marketdata.Dataset, error) {
	var result marketdata.Dataset
	if len(inputs) == 0 || len(inputs) > 24 {
		return result, errors.New("結合する月次分足は1〜24件で指定してください")
	}
	result = inputs[0]
	result.Days = []marketdata.Day{}
	result.ExcludedRows = nil
	format := "jquants-bulk-minute-v0.1"
	if turnover {
		format = marketdata.JQuantsTurnoverFormat
	}
	sources := []marketdata.Source{}
	seenDays := map[string]bool{}
	seenSources := map[string]bool{}
	latest := time.Time{}
	for _, input := range inputs {
		if ctx.Err() != nil {
			return result, errors.New("分足結合を中断しました")
		}
		if marketdata.Validate(input) != nil || input.Symbol != result.Symbol || !strings.HasSuffix(input.Symbol, "@1") || input.Environment != "production" || input.Origin != "observed" || input.Source.Format != format || input.Source.Label != "start" || seenSources[input.Source.SHA256] {
			return result, errors.New("同銘柄の元J-Quants月次分足と重複を確認してください")
		}
		seenSources[input.Source.SHA256] = true
		result.ExcludedRows = append(result.ExcludedRows, input.ExcludedRows...)
		sources = append(sources, input.Source)
		if input.Source.ReceivedAt.After(latest) {
			latest = input.Source.ReceivedAt
		}
		for _, day := range input.Days {
			if seenDays[day.Date] {
				return result, errors.New("月次分足の日付が重複しています")
			}
			seenDays[day.Date] = true
			copyDay := day
			copyDay.Issues = append([]string{}, day.Issues...)
			copyDay.Bars = append([]marketdata.Bar{}, day.Bars...)
			copyDay.Events = append([]marketdata.Event{}, day.Events...)
			for i := range copyDay.Bars {
				copyDay.Bars[i].Issues = append([]string{}, day.Bars[i].Issues...)
				if day.Bars[i].Volume != nil {
					v := *day.Bars[i].Volume
					copyDay.Bars[i].Volume = &v
				}
			}
			for i := range copyDay.Events {
				if day.Events[i].Volume != nil {
					v := *day.Events[i].Volume
					copyDay.Events[i].Volume = &v
				}
			}
			result.Days = append(result.Days, copyDay)
		}
	}
	sort.Slice(result.Days, func(i, j int) bool { return result.Days[i].Date < result.Days[j].Date })
	sort.Slice(sources, func(i, j int) bool { return sources[i].SHA256 < sources[j].SHA256 })
	raw, _ := json.Marshal(sources)
	digest := sha256.Sum256(raw)
	result.Source = marketdata.Source{Format: marketdata.JQuantsJoinedFormat, SHA256: hex.EncodeToString(digest[:]), Label: "start", ReceivedAt: latest}
	result.Converter = Version + "/jquants-month-join-v0.1"
	if turnover {
		result.Source.Format = marketdata.JQuantsTurnoverJoinedFormat
		result.Converter = Version + "/jquants-turnover-month-join-v0.1"
	}
	if err := marketdata.Validate(result); err != nil {
		return marketdata.Dataset{}, err
	}
	return result, nil
}
