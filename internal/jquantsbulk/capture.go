// Package jquantsbulk owns bounded, read-only bulk acquisition. Signed URLs
// never leave memory; downloaded gzip bytes and nonsecret provenance stay local.
package jquantsbulk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const Version = "jquants-bulk-v0.1"
const Endpoint = "/equities/bars/minute"

// Distribution host observed only in authenticated /bulk/get on 2026-10-19.
// Changing storage providers requires an explicit allowlist update.
const storageHost = "450d912298d9da685fb5742a5ec56b76.r2.cloudflarestorage.com"
const maxFileBytes int64 = 512 << 20
const maxTotalBytes int64 = 1 << 30

type File struct {
	Key      string `json:"Key"`
	Modified string `json:"LastModified"`
	Size     int64  `json:"Size"`
	Local    string `json:"local_file,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type Report struct {
	Version      string          `json:"version"`
	Month        string          `json:"month"`
	Started      time.Time       `json:"started_at"`
	Captured     time.Time       `json:"captured_at"`
	Complete     bool            `json:"complete"`
	ListOnly     bool            `json:"list_only"`
	CalendarOnly bool            `json:"calendar_only,omitempty"`
	Calendar     json.RawMessage `json:"calendar,omitempty"`
	Requests     int             `json:"api_requests"`
	LastStatus   int             `json:"last_http_status"`
	Files        []File          `json:"files"`
}

func ValidateMonth(month string) error {
	at, err := time.Parse("2006-01", month)
	if err != nil || at.Format("2006-01") != month || month < "2024-11" {
		return errors.New("bulk monthは2024-11以降のYYYY-MMで指定してください")
	}
	return nil
}

type client struct {
	http       *http.Client
	key, month string
	last       time.Time
	interval   time.Duration
	report     *Report
	keys       map[string]bool
}

func (c *client) metadata(ctx context.Context, path string, q url.Values) ([]byte, error) {
	allowed := false
	if path == "/v2/bulk/list" && len(q) == 3 {
		allowed = q.Get("endpoint") == Endpoint && q.Get("from") == c.month && q.Get("to") == c.month
	} else if path == "/v2/bulk/get" && len(q) == 1 {
		allowed = c.keys[q.Get("key")]
	} else if path == "/v2/markets/calendar" && len(q) == 2 {
		first, _ := time.Parse("2006-01", c.month)
		allowed = q.Get("from") == first.Format("2006-01-02") && q.Get("to") == first.AddDate(0, 1, -1).Format("2006-01-02")
	}
	for _, values := range q {
		if len(values) != 1 {
			allowed = false
		}
	}
	if !allowed || c.report.Requests >= 32 || (c.report.LastStatus != 0 && c.report.LastStatus != 200) {
		return nil, errors.New("bulkの許可範囲/要求上限/停止状態を確認してください")
	}
	if delay := time.Until(c.last.Add(c.interval)); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, errors.New("bulk取得を中断しました")
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.jquants.com"+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("bulk要求を作成できません")
	}
	req.Header.Set("x-api-key", c.key)
	c.last = time.Now()
	c.report.Requests++
	resp, err := c.http.Do(req)
	if err != nil {
		c.report.LastStatus = 502
		return nil, errors.New("bulk API通信に失敗しました（本文非表示）")
	}
	defer resp.Body.Close()
	c.report.LastStatus = resp.StatusCode
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bulk APIを停止しました: status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		c.report.LastStatus = 502
		return nil, errors.New("bulkメタデータの読込上限を超えました")
	}
	return body, nil
}

func validKey(key, month string) bool {
	return strings.HasPrefix(key, "equities/bars/minute/") && strings.HasSuffix(key, ".csv.gz") &&
		!strings.Contains(key, "..") && !strings.ContainsAny(key, "\\\x00\r\n?#") &&
		(strings.Contains(key, strings.ReplaceAll(month, "-", "")) || strings.Contains(key, month))
}

func sizeBytes(number json.Number) (int64, bool) {
	if len(number) == 0 || len(number) > 40 {
		return 0, false
	}
	if i := strings.IndexAny(string(number), "eE"); i >= 0 {
		if exponent, err := strconv.Atoi(string(number)[i+1:]); err != nil || exponent > 128 || exponent < -128 {
			return 0, false
		}
	}
	r, ok := new(big.Rat).SetString(string(number))
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	return r.Num().Int64(), true
}

func signedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" ||
		u.Hostname() != storageHost || u.RawQuery == "" {
		return nil, errors.New("bulk配信先が許可したHTTPSホストと一致しません")
	}
	return u, nil
}

func (c *client) download(ctx context.Context, dir string, file *File) error {
	body, err := c.metadata(ctx, "/v2/bulk/get", url.Values{"key": {file.Key}})
	if err != nil {
		return err
	}
	defer clear(body)
	var link struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(body, &link) != nil {
		return errors.New("bulk URL応答の形式を確認してください")
	}
	u, err := signedURL(link.URL)
	if err != nil {
		if candidate, parseErr := url.Parse(link.URL); parseErr == nil && candidate.Scheme == "https" && candidate.User == nil {
			return fmt.Errorf("bulk配信先が許可リスト外です: hostname=%s（署名URLは非表示）", candidate.Hostname())
		}
		return err
	}
	// A separate request deliberately carries no API key to object storage.
	downloadCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(downloadCtx, "GET", u.String(), nil)
	if err != nil {
		return errors.New("bulkファイル要求を作成できません")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("bulkファイル取得に失敗しました（URL非表示）")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || (resp.ContentLength >= 0 && resp.ContentLength != file.Size) {
		return errors.New("bulkファイルの応答/サイズが一覧と一致しません")
	}
	f, err := os.CreateTemp(dir, ".bulk-*.tmp")
	if err != nil {
		return errors.New("bulkファイルを保存できません")
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, file.Size+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != file.Size {
		return errors.New("bulk保存が未完了、または一覧サイズと不一致です")
	}
	base := filepath.Base(f.Name())
	final := filepath.Join(dir, base[1:len(base)-4]+".csv.gz")
	if os.Rename(f.Name(), final) != nil {
		return errors.New("bulk保存を確定できません")
	}
	file.Local, file.SHA256 = filepath.Base(final), hex.EncodeToString(h.Sum(nil))
	return nil
}

// Capture is sequential and must be called under the shared J-Quants gate.
// Listing precedes all downloads; no retry, login, upload, or purchase occurs.
func Capture(ctx context.Context, key, month, directory string, listOnly, calendarOnly bool) (Report, error) {
	r := Report{Version: Version, Month: month, Started: time.Now().UTC(), ListOnly: listOnly, CalendarOnly: calendarOnly, Files: []File{}}
	if err := ValidateMonth(month); err != nil {
		return r, err
	}
	if len(key) == 0 || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
		return r, errors.New("J-Quantsキーの形式を確認してください")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	defer transport.CloseIdleConnections()
	c := &client{key: key, month: month, interval: 1250 * time.Millisecond, report: &r, keys: map[string]bool{}, http: &http.Client{Transport: transport, Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}
	var err error
	if calendarOnly {
		first, _ := time.Parse("2006-01", month)
		r.Calendar, err = c.metadata(ctx, "/v2/markets/calendar", url.Values{"from": {first.Format("2006-01-02")}, "to": {first.AddDate(0, 1, -1).Format("2006-01-02")}})
		if err == nil && !json.Valid(r.Calendar) {
			err = errors.New("カレンダーJSON形式を確認してください")
			r.Calendar = nil
		}
	} else {
		err = c.capture(ctx, directory, listOnly)
	}
	r.Captured, r.Complete = time.Now().UTC(), err == nil
	return r, err
}

func (c *client) capture(ctx context.Context, directory string, listOnly bool) error {
	body, err := c.metadata(ctx, "/v2/bulk/list", url.Values{"endpoint": {Endpoint}, "from": {c.month}, "to": {c.month}})
	if err != nil {
		return err
	}
	var listing struct {
		Data []struct {
			Key      string      `json:"Key"`
			Modified string      `json:"LastModified"`
			Size     json.Number `json:"Size"`
		} `json:"data"`
	}
	decodeErr := json.Unmarshal(body, &listing)
	if decodeErr != nil || len(listing.Data) == 0 || len(listing.Data) > 31 {
		return fmt.Errorf("bulk一覧の形式/件数を確認してください: rows=%d / json_valid=%t / gzip=%t / decoded=%t", len(listing.Data), json.Valid(body), bytes.HasPrefix(body, []byte{0x1f, 0x8b}), decodeErr == nil)
	}
	var total int64
	files := make([]File, 0, len(listing.Data))
	for _, f := range listing.Data {
		size, sizeOK := sizeBytes(f.Size)
		modified, err := time.Parse(time.RFC3339Nano, f.Modified)
		if !validKey(f.Key, c.month) || c.keys[f.Key] || !sizeOK || size <= 0 || size > maxFileBytes || err != nil || modified.IsZero() {
			return fmt.Errorf("bulk一覧を確認してください: key_valid=%t / compressed_bytes=%d / integer_size=%t / modified_valid=%t", validKey(f.Key, c.month), size, sizeOK, err == nil && !modified.IsZero())
		}
		c.keys[f.Key] = true
		total += size
		files = append(files, File{Key: f.Key, Modified: f.Modified, Size: size})
	}
	if total > maxTotalBytes {
		return errors.New("bulkの総取得上限1GiBを超えています")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Key < files[j].Key })
	c.report.Files = files
	if listOnly {
		return nil
	}
	if os.MkdirAll(directory, 0700) != nil {
		return errors.New("bulk保存先を作成できません")
	}
	for i := range c.report.Files {
		if err := c.download(ctx, directory, &c.report.Files[i]); err != nil {
			return err
		}
	}
	return nil
}
