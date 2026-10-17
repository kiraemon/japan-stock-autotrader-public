// Package capturehistory archives the broker's available trade history and
// describes its coverage. It does not infer missing trades or execute a strategy.
package capturehistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
)

type Reader interface {
	Board(context.Context, string) (json.RawMessage, error)
	TimeAndSales(context.Context, string) (json.RawMessage, error)
}

type Day struct {
	Date      string    `json:"date_jst"`
	Rows      int       `json:"rows"`
	First     time.Time `json:"first_market_time"`
	Last      time.Time `json:"last_market_time"`
	SumVolume float64   `json:"sum_reported_volume"`
	MaxVolume float64   `json:"max_reported_volume"`
}

type Analysis struct {
	Parsed                  bool   `json:"parsed"`
	IdentityMatches         bool   `json:"identity_matches"`
	Rows                    int    `json:"rows"`
	ReportedRows            *int   `json:"reported_rows"`
	CountMatches            bool   `json:"count_matches"`
	InvalidRows             int    `json:"invalid_rows"`
	Days                    []Day  `json:"days"`
	SumVolumeMatchesBracket bool   `json:"today_sum_matches_board_bracket"`
	MaxVolumeMatchesBracket bool   `json:"today_max_matches_board_bracket"`
	VolumeSemantics         string `json:"volume_semantics"`
	FullHistoryVerified     bool   `json:"full_history_verified"`
}

type Archive struct {
	Version           int              `json:"schema_version"`
	Environment       kabu.Environment `json:"environment"`
	Symbol            string           `json:"symbol"`
	StartedAt         time.Time        `json:"started_at"`
	HistoryReceivedAt *time.Time       `json:"history_received_at,omitempty"`
	FinishedAt        time.Time        `json:"finished_at"`
	Complete          bool             `json:"complete"`
	BoardBefore       json.RawMessage  `json:"board_before,omitempty"`
	Trades            json.RawMessage  `json:"time_and_sales,omitempty"`
	BoardAfter        json.RawMessage  `json:"board_after,omitempty"`
	Error             string           `json:"error,omitempty"`
	Analysis          Analysis         `json:"analysis"`
}

func Capture(ctx context.Context, client Reader, environment kabu.Environment, symbols []string, directory string, out io.Writer) error {
	return capture(ctx, client, environment, symbols, directory, out, time.Now, nil)
}

// capture is also used by the aligned owner. The clock and metadata observer
// allow timing tests without a second API client or different archive schema.
func capture(ctx context.Context, client Reader, environment kabu.Environment, symbols []string, directory string, out io.Writer, now func() time.Time, observe func(CaptureTiming) error) error {
	if (environment != kabu.Test && environment != kabu.Production) || len(symbols) < 1 || len(symbols) > 10 {
		return errors.New("履歴取得はtest/production、1〜10銘柄で指定してください")
	}
	seen := map[string]bool{}
	for _, symbol := range symbols {
		if err := kabu.ValidateSymbol(symbol); err != nil {
			return err
		}
		if seen[symbol] {
			return errors.New("履歴銘柄が重複しています")
		}
		seen[symbol] = true
	}
	for _, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := Archive{Version: 1, Environment: environment, Symbol: symbol, StartedAt: now().UTC()}
		var err error
		a.BoardBefore, err = client.Board(ctx, symbol)
		if err == nil {
			a.Trades, err = client.TimeAndSales(ctx, symbol)
			received := now().UTC()
			a.HistoryReceivedAt = &received
		}
		if err == nil {
			a.BoardAfter, err = client.Board(ctx, symbol)
		}
		a.FinishedAt = now().UTC()
		if err != nil {
			a.Error = "履歴または参照板の取得に失敗しました"
		}
		a.Complete = err == nil
		a.Analysis = Analyze(a)
		path, saveErr := save(directory, a)
		if saveErr != nil {
			return saveErr
		}
		if observe != nil {
			info, statErr := os.Stat(path)
			if statErr != nil {
				return errors.New("履歴保存結果の情報を確認できません")
			}
			hash, hashErr := archiveDigest(path)
			if hashErr != nil {
				return hashErr
			}
			if observeErr := observe(CaptureTiming{Symbol: symbol, File: filepath.Base(path), SHA256: hash, StartedAt: a.StartedAt, ReceivedAt: a.HistoryReceivedAt, HTTPFinishedAt: a.FinishedAt, SavedAt: now().UTC(), Bytes: info.Size(), Complete: a.Complete}); observeErr != nil {
				return observeErr
			}
		}
		if _, writeErr := fmt.Fprintf(out, "履歴保存: %s / complete=%t / rows=%d / count_matches=%t / invalid_rows=%d\n", path, a.Complete, a.Analysis.Rows, a.Analysis.CountMatches, a.Analysis.InvalidRows); writeErr != nil {
			return errors.New("履歴の保存結果を出力できません")
		}
		if err != nil {
			return err
		}
		if !a.Analysis.Parsed || !a.Analysis.IdentityMatches || !a.Analysis.CountMatches || a.Analysis.InvalidRows > 0 {
			return errors.New("履歴の形式・銘柄・件数・値を確認してください（原応答はJSONに保存済み）")
		}
	}
	return nil
}

func archiveDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("履歴保存結果のハッシュを確認できません")
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.New("履歴保存結果のハッシュを確認できません")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Analyze only describes returned data. Matching the board's cumulative volume
// is evidence about field interpretation, not proof that every trade is present.
func Analyze(a Archive) Analysis {
	result := Analysis{Days: []Day{}, VolumeSemantics: "unverified"}
	var response struct {
		Symbol            string
		Exchange          json.RawMessage
		TradingPriceCount *int
		TradingPrice      *[]struct {
			Time   string
			Volume json.Number
			Price  json.Number
		}
	}
	if json.Unmarshal(a.Trades, &response) != nil || response.TradingPrice == nil {
		return result
	}
	result.Parsed = true
	parts := strings.Split(a.Symbol, "@")
	market := strings.TrimSpace(string(response.Exchange))
	var marketString string
	if json.Unmarshal(response.Exchange, &marketString) == nil {
		market = marketString
	}
	result.IdentityMatches = len(parts) == 2 && response.Symbol == parts[0] && market == parts[1]
	result.Rows = len(*response.TradingPrice)
	result.ReportedRows = response.TradingPriceCount
	result.CountMatches = response.TradingPriceCount != nil && *response.TradingPriceCount >= 0 && *response.TradingPriceCount == result.Rows
	days := map[string]*Day{}
	for _, row := range *response.TradingPrice {
		t, err := time.Parse(time.RFC3339Nano, row.Time)
		volume, ve := row.Volume.Float64()
		price, pe := row.Price.Float64()
		if err != nil || ve != nil || pe != nil || volume < 0 || price <= 0 || math.IsNaN(volume) || math.IsInf(volume, 0) || math.IsNaN(price) || math.IsInf(price, 0) || t.After(a.FinishedAt.Add(2*time.Second)) {
			result.InvalidRows++
			continue
		}
		date := t.In(time.FixedZone("JST", 9*3600)).Format("2006-01-02")
		d := days[date]
		if d == nil {
			d = &Day{Date: date, First: t, Last: t}
			days[date] = d
		}
		d.Rows++
		d.SumVolume += volume
		if volume > d.MaxVolume {
			d.MaxVolume = volume
		}
		if t.Before(d.First) {
			d.First = t
		}
		if t.After(d.Last) {
			d.Last = t
		}
	}
	for _, d := range days {
		result.Days = append(result.Days, *d)
	}
	sort.Slice(result.Days, func(i, j int) bool { return result.Days[i].Date < result.Days[j].Date })
	// The reference board must identify the same instrument and bracket the
	// history request. Missing values never become a zero-volume match.
	boardVolume := func(raw json.RawMessage) (float64, bool) {
		var b struct {
			Symbol        string
			Exchange      int
			TradingVolume *json.Number
		}
		if json.Unmarshal(raw, &b) != nil || b.TradingVolume == nil || len(parts) != 2 || b.Symbol != parts[0] || strconv.Itoa(b.Exchange) != parts[1] {
			return 0, false
		}
		v, e := b.TradingVolume.Float64()
		return v, e == nil && v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	before, bok := boardVolume(a.BoardBefore)
	after, aok := boardVolume(a.BoardAfter)
	today := a.StartedAt.In(time.FixedZone("JST", 9*3600)).Format("2006-01-02")
	if d := days[today]; d != nil && a.Complete && result.InvalidRows == 0 && bok && aok && after >= before {
		result.SumVolumeMatchesBracket = d.SumVolume >= before && d.SumVolume <= after
		result.MaxVolumeMatchesBracket = d.MaxVolume >= before && d.MaxVolume <= after
	}
	return result
}

func save(directory string, a Archive) (string, error) {
	if os.MkdirAll(directory, 0700) != nil {
		return "", errors.New("履歴保存先を作成できません")
	}
	f, err := os.CreateTemp(directory, ".history-*.tmp")
	if err != nil {
		return "", errors.New("履歴ファイルを作成できません")
	}
	path := f.Name()
	defer os.Remove(path)
	if json.NewEncoder(f).Encode(a) != nil {
		f.Close()
		return "", errors.New("履歴JSONを生成できません")
	}
	if f.Sync() != nil {
		f.Close()
		return "", errors.New("履歴JSONを同期できません")
	}
	if f.Close() != nil {
		return "", errors.New("履歴JSONを閉じられません")
	}
	final := filepath.Join(directory, strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), ".tmp"), ".")+".json")
	if err := os.Rename(path, final); err != nil {
		return "", fileFinalizeError("履歴JSONを確定できません", err)
	}
	return final, nil
}

// Retain the numeric cause without exposing a PathError's filenames or body.
func fileFinalizeError(message string, err error) error {
	var code syscall.Errno
	if errors.As(err, &code) {
		return fmt.Errorf("%s（OSコード=%d）", message, uint64(code))
	}
	return errors.New(message)
}
