package buildbars

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

type csvLineLimit struct {
	input        io.Reader
	sinceNewline int
}

func (r *csvLineLimit) Read(p []byte) (int, error) {
	n, err := r.input.Read(p)
	for i, b := range p[:n] {
		if b == '\n' {
			r.sinceNewline = 0
		} else {
			r.sinceNewline++
			if r.sinceNewline > 65536 {
				return i, errors.New("CSV行の長さ上限を超えました")
			}
		}
	}
	return n, err
}

// FromJQuantsBulkCSV consumes an already decompressed CSV stream. The caller
// verifies the compressed bytes against sourceSHA and keeps the original gzip.
// Unselected rows are structurally read but their market values are not audited.
func FromJQuantsBulkCSV(ctx context.Context, input io.Reader, sourceSHA, month string, captured time.Time, symbols []string) ([]marketdata.Dataset, error) {
	return fromJQuantsBulkCSV(ctx, input, sourceSHA, month, captured, symbols, false)
}

// FromJQuantsBulkCSVWithTurnover is an explicit additional contract. Selected
// 2024-11-01 rows are counted as unsupported, never silently re-timed or filled.
func FromJQuantsBulkCSVWithTurnover(ctx context.Context, input io.Reader, sourceSHA, month string, captured time.Time, symbols []string) ([]marketdata.Dataset, error) {
	return fromJQuantsBulkCSV(ctx, input, sourceSHA, month, captured, symbols, true)
}

func fromJQuantsBulkCSV(ctx context.Context, input io.Reader, sourceSHA, month string, captured time.Time, symbols []string, turnover bool) ([]marketdata.Dataset, error) {
	first, err := time.Parse("2006-01", month)
	minimum := "2024-12"
	if turnover {
		minimum = "2024-11"
	}
	if err != nil || first.Format("2006-01") != month || month < minimum || captured.IsZero() || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sourceSHA) || len(symbols) == 0 || len(symbols) > 50 {
		return nil, errors.New("bulk変換の月/取得時刻/原hash/銘柄を確認してください")
	}
	byCode := map[string][]jqRow{}
	excluded := map[string]int{}
	pattern := regexp.MustCompile(`^[0-9A-Z]{4}@1$`)
	for _, symbol := range symbols {
		code := strings.TrimSuffix(symbol, "@1")
		if _, ok := byCode[code]; ok || !pattern.MatchString(symbol) {
			return nil, errors.New("bulk変換の銘柄形式/重複を確認してください")
		}
		byCode[code] = []jqRow{}
	}
	limited := &io.LimitedReader{R: input, N: (4 << 30) + 1}
	r := csv.NewReader(&csvLineLimit{input: limited})
	r.FieldsPerRecord = 9
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil || strings.Join(header, ",") != "Date,Time,Code,O,H,L,C,Vo,Va" {
		return nil, errors.New("J-Quants bulk CSVの見出しを確認してください")
	}
	for line := 2; ; line++ {
		if ctx.Err() != nil {
			return nil, errors.New("bulk CSV変換を中断しました")
		}
		values, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || line > 30000000 {
			return nil, errors.New("bulk CSVの形式/読込上限/圧縮整合性を確認してください")
		}
		code := values[2]
		if len(code) == 5 && code[4] == '0' {
			code = code[:4]
		}
		if _, ok := byCode[code]; !ok {
			continue
		}
		if turnover && values[0] < "2024-11-05" {
			if month != "2024-11" || values[0] != "2024-11-01" {
				return nil, errors.New("売買代金付き分足の対応開始日を確認してください")
			}
			clock := values[1]
			if len(clock) == 5 {
				clock += ":00"
			}
			at, e := time.ParseInLocation("2006-01-02 15:04:05", values[0]+" "+clock, marketdata.JST)
			if e != nil || !at.Equal(at.Truncate(time.Minute)) || at.After(captured) || excluded[code] >= 20000 {
				return nil, errors.New("対象外の旧立会日の行形式/件数を確認してください")
			}
			excluded[code]++
			continue
		}
		if len(byCode[code]) >= 20000 {
			return nil, errors.New("bulk選択銘柄の行数上限を超えました")
		}
		byCode[code] = append(byCode[code], jqRow{Date: values[0], Time: values[1], Code: values[2], Open: json.Number(values[3]), High: json.Number(values[4]), Low: json.Number(values[5]), Close: json.Number(values[6]), Volume: json.Number(values[7]), Turnover: json.Number(values[8]), SourceRow: line})
	}
	if limited.N <= 0 {
		return nil, errors.New("bulk展開上限4GiBを超えました")
	}
	datasets := make([]marketdata.Dataset, 0, len(symbols))
	for _, symbol := range symbols {
		code := strings.TrimSuffix(symbol, "@1")
		if len(byCode[code]) == 0 {
			return nil, errors.New("bulkに指定銘柄の分足がありません")
		}
		d := dataset(nil, symbol, "production", "jquants-bulk-minute-v0.1", captured)
		d.Source.SHA256 = sourceSHA
		d.Converter = Version + "/jquants-bulk-v0.1"
		if turnover {
			d.Source.Format = marketdata.JQuantsTurnoverFormat
			d.Converter = Version + "/jquants-bulk-turnover-v0.1"
			if excluded[code] > 0 {
				d.ExcludedRows = []marketdata.SourceExclusion{{Date: "2024-11-01", Rows: excluded[code], Reason: "session_before_supported_20241105"}}
			}
		}
		d, err = jquantsDataset(ctx, d, code, first.Format("2006-01-02"), first.AddDate(0, 1, -1).Format("2006-01-02"), byCode[code])
		if err != nil {
			return nil, err
		}
		datasets = append(datasets, d)
	}
	return datasets, nil
}
