// Package jquantscli runs the pinned official CLI behind a bounded read-only
// bridge. The real API key stays in this process, never in CLI logs or arguments.
package jquantscli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const Version = "jquants-cli-capture-v0.1"
const CLIVersion = "1.6.0"
const EXESHA256 = "22109d749d8521a0353c19ff52cdcb85bcab7f0d104c7913d588a08457d1863d"
const maxOutput = 16 << 20

type Job struct{ Code, From, To string }

type Result struct {
	Code     string          `json:"code"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	Started  time.Time       `json:"started_at"`
	Captured time.Time       `json:"captured_at"`
	Data     json.RawMessage `json:"data"`
}

type Stats struct {
	Requests   int `json:"api_requests"`
	LastStatus int `json:"last_http_status"`
	IntervalMS int `json:"minimum_request_interval_ms"`
}

func ValidateJobs(jobs []Job) error {
	if len(jobs) == 0 || len(jobs) > 3 {
		return errors.New("初回取得は1〜3銘柄に限定しています")
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		from, a := time.Parse("2006-01-02", j.From)
		to, b := time.Parse("2006-01-02", j.To)
		if !regexp.MustCompile(`^[0-9A-Z]{4}$`).MatchString(j.Code) || seen[j.Code] || a != nil || b != nil || from.Format("2006-01-02") != j.From || to.Format("2006-01-02") != j.To || to.Before(from) || to.Sub(from) > 30*24*time.Hour {
			return errors.New("銘柄の重複・日付・31日以内の取得範囲を確認してください")
		}
		seen[j.Code] = true
	}
	return nil
}

type bridge struct {
	key, nonce string
	jobs       []Job
	client     *http.Client
	mu         sync.Mutex
	last       time.Time
	interval   time.Duration
	stats      Stats
	budget     int
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/v2/equities/bars/minute" || r.Header.Get("x-api-key") != b.nonce {
		http.Error(w, "request refused", http.StatusForbidden)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "query refused", 400)
		return
	}
	for k, values := range q {
		if len(values) != 1 || (k != "code" && k != "from" && k != "to" && k != "pagination_key") || len(values[0]) > 4096 {
			http.Error(w, "query refused", 400)
			return
		}
	}
	allowed := false
	for _, j := range b.jobs {
		if q.Get("code") == j.Code && q.Get("from") == j.From && q.Get("to") == j.To {
			allowed = true
		}
	}
	if !allowed {
		http.Error(w, "range refused", 403)
		return
	}
	// Serialize every upstream request, including CLI-internal pagination.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stats.Requests >= b.budget || (b.stats.LastStatus != 0 && b.stats.LastStatus != 200 && b.stats.LastStatus != 210) {
		http.Error(w, "request budget stopped", 429)
		return
	}
	if wait := time.Until(b.last.Add(b.interval)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://api.jquants.com/v2/equities/bars/minute?"+q.Encode(), nil)
	if err != nil {
		http.Error(w, "request failed", 502)
		return
	}
	upstream.Header.Set("x-api-key", b.key)
	b.last = time.Now()
	b.stats.Requests++
	response, err := b.client.Do(upstream)
	if err != nil {
		b.stats.LastStatus = 502
		http.Error(w, "upstream failed", 502)
		return
	}
	defer response.Body.Close()
	b.stats.LastStatus = response.StatusCode
	if response.StatusCode != 200 && response.StatusCode != 210 {
		http.Error(w, "upstream refused", response.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxOutput+1))
	if err != nil || len(body) > maxOutput {
		b.stats.LastStatus = 502
		http.Error(w, "response exceeded limit", 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

func childEnvironment(base []string, nonce, baseURL string) []string {
	result := []string{}
	for _, entry := range base {
		k, _, _ := strings.Cut(entry, "=")
		k = strings.ToUpper(k)
		if strings.HasPrefix(k, "OP_") || strings.HasPrefix(k, "JQUANTS_") || k == "HTTP_PROXY" || k == "HTTPS_PROXY" || k == "ALL_PROXY" {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "JQUANTS_API_KEY="+nonce, "JQUANTS_BASE_URL="+baseURL, "NO_PROXY=127.0.0.1,localhost")
}

type limitedOutput struct {
	data     []byte
	overflow bool
}

func (o *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxOutput - len(o.data)
	if len(p) > remaining {
		o.overflow = true
		p = p[:remaining]
	}
	o.data = append(o.data, p...)
	return n, nil
}

// Capture owns one loopback bridge for the whole batch. The caller must acquire
// an account-wide J-Quants gate before authentication. No auth/login commands run.
func Capture(ctx context.Context, executable, key string, jobs []Job) ([]Result, Stats, error) {
	if err := ValidateJobs(jobs); err != nil {
		return nil, Stats{}, err
	}
	if len(key) == 0 || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
		return nil, Stats{}, errors.New("J-Quantsキーの形式を確認してください")
	}
	// The official CLI prefers this plaintext file over its environment variable.
	// Refuse it without reading or deleting it; never silently select another key.
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, Stats{}, errors.New("CLI認証保存先を確認できません")
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "jquants", "credentials.json")); !errors.Is(err, os.ErrNotExist) {
		return nil, Stats{}, errors.New("既存CLIの認証ファイルがあるためキー選択を中止しました（内容は読み取りません）")
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, Stats{}, errors.New("取得セッションを作成できません")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	b := &bridge{key: key, nonce: hex.EncodeToString(nonceBytes), jobs: append([]Job{}, jobs...), interval: 1250 * time.Millisecond, budget: 32, stats: Stats{IntervalMS: 1250}, client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, Stats{}, errors.New("取得窓口を起動できません")
	}
	server := &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	results := []Result{}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return results, b.snapshot(), errors.New("J-Quants取得を中断しました")
		}
		started := time.Now().UTC()
		cmd := exec.CommandContext(ctx, executable, "--output", "json", "eq", "minute", "--code", j.Code, "--from", j.From, "--to", j.To)
		cmd.Dir = filepath.Dir(executable)
		cmd.Env = childEnvironment(os.Environ(), b.nonce, "http://"+listener.Addr().String()+"/v2")
		var output limitedOutput
		cmd.Stdout, cmd.Stderr = &output, io.Discard
		if err := cmd.Run(); err != nil || output.overflow {
			clear(output.data)
			return results, b.snapshot(), errors.New("公式CLI取得に失敗しました（認証/応答本文は出力しません）")
		}
		var rows []json.RawMessage
		if json.Unmarshal(output.data, &rows) != nil || rows == nil || !bytes.HasPrefix(bytes.TrimSpace(output.data), []byte("[")) {
			clear(output.data)
			return results, b.snapshot(), errors.New("公式CLIのJSON配列を確認できません")
		}
		results = append(results, Result{Code: j.Code, From: j.From, To: j.To, Started: started, Captured: time.Now().UTC(), Data: json.RawMessage(output.data)})
	}
	return results, b.snapshot(), nil
}

func (b *bridge) snapshot() Stats { b.mu.Lock(); defer b.mu.Unlock(); return b.stats }
