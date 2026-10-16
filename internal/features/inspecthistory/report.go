// Package inspecthistory audits archived trades using saved quotes only.
// It never authenticates, infers missing trades, or declares a dataset complete.
package inspecthistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var jst = time.FixedZone("JST", 9*3600)
var symbolPattern = regexp.MustCompile(`^[0-9A-Z]{4}@(1|3|5|6)$`)

type Report struct {
	Version                   int       `json:"report_version"`
	Environment               string    `json:"environment"`
	CreatedAt                 time.Time `json:"created_at"`
	Archives                  []Check   `json:"archives"`
	InvalidSnapshots          int       `json:"invalid_snapshots"`
	FullHistoryVerified       bool      `json:"full_history_verified"`
	IndependentSourceVerified bool      `json:"independent_source_verified"`
}

type Check struct {
	File           string `json:"file"`
	SHA256         string `json:"sha256"`
	Symbol         string `json:"symbol"`
	Valid          bool   `json:"valid"`
	Rows           int    `json:"rows"`
	BackwardSteps  int    `json:"backward_steps"`
	EqualTimeSteps int    `json:"equal_time_steps"`
	Days           []Day  `json:"days"`
}

type Day struct {
	Date                       string    `json:"date_jst"`
	Rows                       int       `json:"rows"`
	First                      time.Time `json:"first_market_time"`
	Last                       time.Time `json:"last_market_time"`
	MinutesWithTrades          int       `json:"minutes_with_trades"`
	UnfinishedMinutes          int       `json:"unfinished_minutes"`
	OpenOrderSensitiveMinutes  int       `json:"open_order_sensitive_minutes"`
	CloseOrderSensitiveMinutes int       `json:"close_order_sensitive_minutes"`
	Reference                  string    `json:"reference,omitempty"`
	ReferenceSHA256            string    `json:"reference_sha256,omitempty"`
	Matches                    *Matches  `json:"matches,omitempty"`
}

type Matches struct {
	Open        bool `json:"open"`
	High        bool `json:"high"`
	Low         bool `json:"low"`
	Last        bool `json:"last"`
	Volume      bool `json:"volume"`
	OpeningTime bool `json:"opening_time"`
	LastTime    bool `json:"last_time"`
}

type board struct {
	Symbol                                                         string
	Exchange                                                       int
	OpeningPrice, HighPrice, LowPrice, CurrentPrice, TradingVolume json.Number
	OpeningPriceTime, CurrentPriceTime, TradingVolumeTime          time.Time
}
type reference struct {
	board      board
	file, hash string
	received   time.Time
}
type archive struct {
	Version     int             `json:"schema_version"`
	Environment string          `json:"environment"`
	Symbol      string          `json:"symbol"`
	Complete    bool            `json:"complete"`
	StartedAt   time.Time       `json:"started_at"`
	ReceivedAt  *time.Time      `json:"history_received_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	BoardBefore json.RawMessage `json:"board_before"`
	BoardAfter  json.RawMessage `json:"board_after"`
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

// Inspect bounds both file counts and bytes; growing collectors do not extend
// the initial directory listing. Reports contain comparisons, never prices.
func Inspect(ctx context.Context, historyDir, snapshotDir, environment string) (Report, error) {
	r := Report{Version: 1, Environment: environment, CreatedAt: time.Now().UTC(), Archives: []Check{}}
	if environment != "test" && environment != "production" {
		return r, errors.New("環境はtestまたはproductionを指定してください")
	}
	files, err := filepath.Glob(filepath.Join(historyDir, "history-*.json"))
	if err != nil || len(files) == 0 || len(files) > 1000 {
		return r, errors.New("履歴ファイルは1〜1000件必要です")
	}
	refs, invalid, err := readReferences(ctx, snapshotDir, environment)
	if err != nil {
		return r, err
	}
	r.InvalidSnapshots = invalid
	var total int64
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return r, errors.New("履歴確認を中断しました")
		}
		raw, err := readFile(file, &total)
		if err != nil {
			return r, err
		}
		r.Archives = append(r.Archives, analyze(raw, filepath.Base(file), environment, refs))
	}
	return r, nil
}

func rat(n json.Number) (*big.Rat, bool) {
	s := string(n)
	if len(s) > 64 {
		return nil, false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(s[i+1:])
		if err != nil || exponent < -18 || exponent > 18 {
			return nil, false
		}
	}
	v, ok := new(big.Rat).SetString(s)
	return v, ok
}
func hash(raw []byte) string  { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func date(t time.Time) string { return t.In(jst).Format("2006-01-02") }
func validBoard(b board, symbol, day string) bool {
	if b.Symbol+"@"+strconv.Itoa(b.Exchange) != symbol || date(b.CurrentPriceTime) != day || date(b.TradingVolumeTime) != day || date(b.OpeningPriceTime) != day {
		return false
	}
	for i, n := range []json.Number{b.OpeningPrice, b.HighPrice, b.LowPrice, b.CurrentPrice, b.TradingVolume} {
		v, ok := rat(n)
		if !ok || v.Sign() < 0 || (i < 4 && v.Sign() == 0) {
			return false
		}
	}
	return true
}
func equal(a *big.Rat, b json.Number) bool { v, ok := rat(b); return ok && a.Cmp(v) == 0 }

type aggregate struct {
	day                           Day
	open, high, low, last, volume *big.Rat
	minutes                       map[time.Time]bool
	edges                         map[time.Time]*minuteEdges
}

// When distinct prices share a boundary timestamp, original array order would
// affect a minute's open or close. The response has no verified sequence key.
type minuteEdges struct {
	first, last                   time.Time
	firstPrice, lastPrice         *big.Rat
	openSensitive, closeSensitive bool
}

func (e *minuteEdges) add(t time.Time, p *big.Rat) {
	if e.first.IsZero() || t.Before(e.first) {
		e.first = t
		e.firstPrice = new(big.Rat).Set(p)
		e.openSensitive = false
	} else if t.Equal(e.first) && p.Cmp(e.firstPrice) != 0 {
		e.openSensitive = true
	}
	if e.last.IsZero() || t.After(e.last) {
		e.last = t
		e.lastPrice = new(big.Rat).Set(p)
		e.closeSensitive = false
	} else if t.Equal(e.last) && p.Cmp(e.lastPrice) != 0 {
		e.closeSensitive = true
	}
}

func analyze(raw []byte, filename, environment string, refs map[string][]reference) Check {
	c := Check{File: filename, SHA256: hash(raw), Days: []Day{}}
	var a archive
	if json.Unmarshal(raw, &a) != nil || a.Version != 1 || a.Environment != environment || !a.Complete || a.Trades.Rows == nil || a.Trades.Count == nil || a.ReceivedAt == nil || a.StartedAt.IsZero() || a.StartedAt.After(*a.ReceivedAt) || a.ReceivedAt.After(a.FinishedAt) {
		return c
	}
	var exchange string
	if json.Unmarshal(a.Trades.Exchange, &exchange) != nil {
		exchange = string(a.Trades.Exchange)
	}
	if !symbolPattern.MatchString(a.Symbol) || a.Symbol != a.Trades.Symbol+"@"+exchange || *a.Trades.Count != len(*a.Trades.Rows) {
		return c
	}
	c.Symbol = a.Symbol
	c.Rows = len(*a.Trades.Rows)
	days := map[string]*aggregate{}
	var previous time.Time
	for _, row := range *a.Trades.Rows {
		p, pok := rat(row.Price)
		v, vok := rat(row.Volume)
		if row.Time.IsZero() || row.Time.After(a.ReceivedAt.Add(2*time.Second)) || !pok || !vok || p.Sign() <= 0 || v.Sign() < 0 {
			return c
		}
		if !previous.IsZero() {
			if row.Time.Before(previous) {
				c.BackwardSteps++
			}
			if row.Time.Equal(previous) {
				c.EqualTimeSteps++
			}
		}
		previous = row.Time
		key := date(row.Time)
		g := days[key]
		if g == nil {
			g = &aggregate{day: Day{Date: key, First: row.Time, Last: row.Time}, open: new(big.Rat).Set(p), high: new(big.Rat).Set(p), low: new(big.Rat).Set(p), last: new(big.Rat).Set(p), volume: new(big.Rat), minutes: map[time.Time]bool{}, edges: map[time.Time]*minuteEdges{}}
			days[key] = g
		}
		g.day.Rows++
		g.volume.Add(g.volume, v)
		g.minutes[row.Time.Truncate(time.Minute)] = true
		minute := row.Time.Truncate(time.Minute)
		if g.edges[minute] == nil {
			g.edges[minute] = &minuteEdges{}
		}
		g.edges[minute].add(row.Time, p)
		if row.Time.Before(g.day.First) {
			g.day.First = row.Time
			g.open.Set(p)
		}
		if !row.Time.Before(g.day.Last) {
			g.day.Last = row.Time
			g.last.Set(p)
		}
		if p.Cmp(g.high) > 0 {
			g.high.Set(p)
		}
		if p.Cmp(g.low) < 0 {
			g.low.Set(p)
		}
	}
	c.Valid = true
	for key, g := range days {
		g.day.MinutesWithTrades = len(g.minutes)
		for _, edge := range g.edges {
			if edge.openSensitive {
				g.day.OpenOrderSensitiveMinutes++
			}
			if edge.closeSensitive {
				g.day.CloseOrderSensitiveMinutes++
			}
		}
		for minute := range g.minutes {
			if minute.Add(time.Minute).After(*a.ReceivedAt) {
				g.day.UnfinishedMinutes++
			}
		}
		var ref *reference
		// Before/after quotes are the closest available observations for today.
		for i, braw := range []json.RawMessage{a.BoardBefore, a.BoardAfter} {
			var b board
			if json.Unmarshal(braw, &b) == nil && validBoard(b, a.Symbol, key) && !b.CurrentPriceTime.After(a.FinishedAt.Add(2*time.Second)) && !b.TradingVolumeTime.After(a.FinishedAt.Add(2*time.Second)) {
				name := "board_before"
				if i == 1 {
					name = "board_after"
				}
				ref = &reference{board: b, file: filename + ":" + name, hash: c.SHA256}
			}
		}
		if ref == nil && key < date(a.StartedAt) {
			for _, candidate := range refs[a.Symbol+"/"+key] {
				if !candidate.received.After(a.FinishedAt) && (ref == nil || candidate.received.After(ref.received)) {
					selected := candidate
					ref = &selected
				}
			}
		}
		if ref != nil {
			b := ref.board
			g.day.Reference = ref.file
			g.day.ReferenceSHA256 = ref.hash
			g.day.Matches = &Matches{Open: equal(g.open, b.OpeningPrice), High: equal(g.high, b.HighPrice), Low: equal(g.low, b.LowPrice), Last: equal(g.last, b.CurrentPrice), Volume: equal(g.volume, b.TradingVolume), OpeningTime: g.day.First.Equal(b.OpeningPriceTime), LastTime: g.day.Last.Equal(b.CurrentPriceTime)}
		}
		c.Days = append(c.Days, g.day)
	}
	sort.Slice(c.Days, func(i, j int) bool { return c.Days[i].Date < c.Days[j].Date })
	return c
}

func readReferences(ctx context.Context, directory, environment string) (map[string][]reference, int, error) {
	refs := map[string][]reference{}
	invalid := 0
	files, err := filepath.Glob(filepath.Join(directory, "snapshot-*.json"))
	if err != nil || len(files) > 10000 {
		return nil, 0, errors.New("照合用スナップショットの一覧を確認できません")
	}
	var total int64
	for _, file := range files {
		if ctx.Err() != nil {
			return nil, 0, errors.New("履歴確認を中断しました")
		}
		raw, err := readFile(file, &total)
		if err != nil {
			return nil, 0, err
		}
		var s struct {
			Version     int       `json:"schema_version"`
			Environment string    `json:"environment"`
			Complete    bool      `json:"complete"`
			StartedAt   time.Time `json:"started_at"`
			FinishedAt  time.Time `json:"finished_at"`
			Records     []struct {
				Endpoint string          `json:"endpoint"`
				Received time.Time       `json:"received_at"`
				Data     json.RawMessage `json:"data"`
				Error    string          `json:"error"`
			} `json:"records"`
		}
		if json.Unmarshal(raw, &s) != nil || s.Version != 1 || s.Environment != environment || !s.Complete || s.StartedAt.IsZero() || s.FinishedAt.Before(s.StartedAt) {
			invalid++
			continue
		}
		for _, record := range s.Records {
			var b board
			if record.Error != "" || record.Received.Before(s.StartedAt) || record.Received.After(s.FinishedAt.Add(2*time.Second)) || json.Unmarshal(record.Data, &b) != nil {
				continue
			}
			symbol := b.Symbol + "@" + strconv.Itoa(b.Exchange)
			day := date(b.CurrentPriceTime)
			if record.Endpoint != "/board/"+symbol || !validBoard(b, symbol, day) || b.CurrentPriceTime.After(record.Received.Add(2*time.Second)) || b.TradingVolumeTime.After(record.Received.Add(2*time.Second)) {
				continue
			}
			refs[symbol+"/"+day] = append(refs[symbol+"/"+day], reference{board: b, file: filepath.Base(file), hash: hash(raw), received: record.Received})
		}
	}
	return refs, invalid, nil
}

func readFile(path string, total *int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 || *total+info.Size() > 256<<20 {
		return nil, errors.New("確認用ファイルを読み取れません（各64MiB・合計256MiB上限）")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("確認用ファイルを開けません")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	*total += int64(len(raw))
	if err != nil || len(raw) > 64<<20 || *total > 256<<20 {
		return nil, errors.New("確認用ファイルの読取上限を超えました")
	}
	return raw, nil
}

func Save(directory string, r Report) (string, error) {
	if os.MkdirAll(directory, 0700) != nil {
		return "", errors.New("履歴確認の保存先を作成できません")
	}
	f, err := os.CreateTemp(directory, ".history-check-*.tmp")
	if err != nil {
		return "", errors.New("履歴確認ファイルを作成できません")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if json.NewEncoder(f).Encode(r) != nil {
		f.Close()
		return "", errors.New("履歴確認JSONを生成できません")
	}
	if f.Sync() != nil {
		f.Close()
		return "", errors.New("履歴確認JSONを同期できません")
	}
	if f.Close() != nil {
		return "", errors.New("履歴確認JSONを閉じられません")
	}
	final := filepath.Join(directory, filepath.Base(tmp)[1:len(filepath.Base(tmp))-4]+".json")
	if os.Rename(tmp, final) != nil {
		return "", errors.New("履歴確認JSONを確定できません")
	}
	return final, nil
}
