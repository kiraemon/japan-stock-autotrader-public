// Package dashboard presents a restricted view of saved data, without broker access.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Quality struct {
	Available     bool `json:"available"`
	NeedsReview   bool `json:"needs_review"`
	InvalidFiles  int  `json:"invalid_files"`
	MissingPrices int  `json:"missing_prices"`
	MissingTimes  int  `json:"missing_times"`
	APIErrors     int  `json:"api_errors"`
}

type Point struct {
	Time  time.Time `json:"time"`
	Price float64   `json:"price"`
}

type Quote struct {
	Symbol     string     `json:"symbol"`
	Name       string     `json:"name"`
	Price      *float64   `json:"price"`
	MarketTime *time.Time `json:"market_time"`
	ReceivedAt time.Time  `json:"received_at"`
	Points     []Point    `json:"points"`
}

type Recent struct {
	FinishedAt time.Time `json:"finished_at"`
	Complete   bool      `json:"complete"`
	Records    int       `json:"records"`
}

// View is the complete browser contract. Account fields and source errors are absent.
type View struct {
	Environment     string     `json:"environment"`
	Demo            bool       `json:"demo"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LatestAt        *time.Time `json:"latest_at"`
	Files           int        `json:"files"`
	CompleteFiles   int        `json:"complete_files"`
	IncompleteFiles int        `json:"incomplete_files"`
	InvalidFiles    int        `json:"invalid_files"`
	Bytes           int64      `json:"bytes"`
	Quotes          []Quote    `json:"quotes"`
	Recent          []Recent   `json:"recent"`
	Quality         Quality    `json:"quality"`
}

type snapshot struct {
	Version     int       `json:"schema_version"`
	Environment string    `json:"environment"`
	Started     time.Time `json:"started_at"`
	Finished    time.Time `json:"finished_at"`
	Complete    bool      `json:"complete"`
	RecordCount int       `json:"-"`
	Records     []struct {
		Endpoint string          `json:"endpoint"`
		Received time.Time       `json:"received_at"`
		Data     json.RawMessage `json:"data"`
		Error    string          `json:"error"`
	} `json:"records"`
}

// Read projects only board fields and metadata. It never writes files.
func Read(ctx context.Context, root, environment string, now time.Time) (View, error) {
	v := View{Environment: environment, UpdatedAt: now, Quotes: []Quote{}, Recent: []Recent{}}
	if environment != "production" && environment != "test" {
		return v, errors.New("invalid environment")
	}
	entries, err := os.ReadDir(filepath.Join(root, environment))
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return v, errors.New("保存データを読み込めません")
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "snapshot-") && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(root, environment, entry.Name()))
		}
	}
	if len(paths) > 10000 {
		return v, errors.New("対象ファイルが上限10000件を超えています")
	}
	v.Files = len(paths)
	var runs []snapshot
	for _, path := range paths {
		if ctx.Err() != nil {
			return v, errors.New("表示データの読込を中断しました")
		}
		s, size, err := readOne(path)
		v.Bytes += size
		if v.Bytes > 256<<20 {
			return v, errors.New("表示対象の容量が上限256MiBを超えています")
		}
		if err != nil || s.Version != 1 || s.Environment != environment || s.Started.IsZero() || s.Finished.IsZero() || s.Finished.Before(s.Started) || s.RecordCount == 0 {
			v.InvalidFiles++
			continue
		}
		runs = append(runs, s)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Finished.Before(runs[j].Finished) })
	quotes := map[string]*Quote{}
	for _, run := range runs {
		if run.Complete {
			v.CompleteFiles++
		} else {
			v.IncompleteFiles++
		}
		latest := run.Finished
		v.LatestAt = &latest
		v.Recent = append(v.Recent, Recent{run.Finished, run.Complete, run.RecordCount})
		for _, record := range run.Records {
			if !strings.HasPrefix(record.Endpoint, "/board/") {
				continue
			}
			symbol := strings.TrimPrefix(record.Endpoint, "/board/")
			if !validSymbol(symbol) || record.Received.IsZero() {
				continue
			}
			q := quotes[symbol]
			if q == nil {
				q = &Quote{Symbol: symbol, Name: symbol, Points: []Point{}}
				quotes[symbol] = q
			}
			var board struct {
				Name       string     `json:"SymbolName"`
				Price      *float64   `json:"CurrentPrice"`
				MarketTime *time.Time `json:"CurrentPriceTime"`
			}
			ok := record.Error == "" && json.Unmarshal(record.Data, &board) == nil
			if record.Received.After(q.ReceivedAt) || record.Received.Equal(q.ReceivedAt) {
				q.ReceivedAt = record.Received
				q.Price, q.MarketTime = nil, nil
				if ok {
					if board.Price != nil && *board.Price > 0 {
						q.Price = board.Price
					}
					if board.MarketTime != nil && !board.MarketTime.IsZero() {
						q.MarketTime = board.MarketTime
					}
					if len(board.Name) <= 240 && board.Name != "" {
						q.Name = board.Name
					}
				}
			}
			if ok && board.Price != nil && *board.Price > 0 {
				q.Points = append(q.Points, Point{record.Received, *board.Price})
			}
		}
	}
	for _, quote := range quotes {
		sort.SliceStable(quote.Points, func(i, j int) bool { return quote.Points[i].Time.Before(quote.Points[j].Time) })
		if len(quote.Points) > 60 {
			quote.Points = quote.Points[len(quote.Points)-60:]
		}
		v.Quotes = append(v.Quotes, *quote)
	}
	sort.Slice(v.Quotes, func(i, j int) bool { return v.Quotes[i].Symbol < v.Quotes[j].Symbol })
	if len(v.Quotes) > 50 {
		v.Quotes = v.Quotes[:50]
	}
	if len(v.Recent) > 6 {
		v.Recent = v.Recent[len(v.Recent)-6:]
	}
	for i, j := 0, len(v.Recent)-1; i < j; i, j = i+1, j-1 {
		v.Recent[i], v.Recent[j] = v.Recent[j], v.Recent[i]
	}
	return v, nil
}

func readOne(path string) (snapshot, int64, error) {
	var s snapshot
	f, err := os.Open(path)
	if err != nil {
		return s, 0, errors.New("unreadable snapshot")
	}
	defer f.Close()
	info, err := f.Stat()
	const limit = 64 << 20
	if err != nil || !info.Mode().IsRegular() {
		return s, 0, errors.New("invalid file")
	}
	if info.Size() > limit {
		return s, info.Size(), errors.New("oversized file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &s) != nil {
		return s, int64(len(raw)), errors.New("invalid snapshot")
	}
	s.RecordCount = len(s.Records)
	// Discard account payloads before retaining a run for chronological sorting.
	boards := s.Records[:0]
	for _, record := range s.Records {
		if strings.HasPrefix(record.Endpoint, "/board/") {
			boards = append(boards, record)
		}
	}
	clear(s.Records[len(boards):])
	s.Records = boards
	return s, int64(len(raw)), nil
}

func validSymbol(s string) bool {
	parts := strings.Split(s, "@")
	if len(parts) != 2 || len(parts[0]) != 4 {
		return false
	}
	switch parts[1] {
	case "1", "3", "5", "6":
	default:
		return false
	}
	for _, c := range parts[0] {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// Demo is synthetic, independent of the filesystem and broker.
func Demo(environment string, now time.Time) View {
	price := 1542.5
	market := now.Add(-3 * time.Minute)
	latest := now.Add(-5 * time.Second)
	v := View{Environment: environment, Demo: true, UpdatedAt: now, LatestAt: &latest, Files: 128, CompleteFiles: 127, IncompleteFiles: 1, Bytes: 1542016, Quality: Quality{Available: true, NeedsReview: true, APIErrors: 1}, Quotes: []Quote{}, Recent: []Recent{}}
	q := Quote{Symbol: "0000@1", Name: "デモ銘柄 A", Price: &price, MarketTime: &market, ReceivedAt: latest, Points: []Point{}}
	for i := 0; i < 40; i++ {
		q.Points = append(q.Points, Point{now.Add(time.Duration(i-40) * 10 * time.Second), 1500 + float64(i) + float64((i*7)%13)})
	}
	q.Points[len(q.Points)-1].Price = price
	v.Quotes = append(v.Quotes, q)
	for i := 0; i < 6; i++ {
		v.Recent = append(v.Recent, Recent{latest.Add(-time.Duration(i) * 11 * time.Second), i != 3, 5})
	}
	return v
}
