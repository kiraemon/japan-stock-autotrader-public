// Package kabu provides a deliberately restricted, read-only trading API client.
package kabu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/instanceguard"
)

type Environment string

const (
	Test       Environment = "test"
	Production Environment = "production"
)

var symbolPattern = regexp.MustCompile(`^[0-9A-Z]{4}@(1|3|5|6)$`)

func ValidateSymbol(symbol string) error {
	if !symbolPattern.MatchString(symbol) {
		return errors.New("銘柄は 7203@1 の形式で指定してください（市場: 1,3,5,6）")
	}
	return nil
}

// Client contains credentials only in memory. It must not be logged or serialized.
// Requests are serialized and spaced by at least 250ms. No automatic retries.
type Client struct {
	mu         sync.Mutex
	base       string
	http       *http.Client
	token      string
	next       time.Time
	nextTrades time.Time
	closed     bool
	guard      *instanceguard.Guard
}

// Open is the only production constructor. One owner covers both environments,
// independent of working directory, checkout, or Windows terminal session.
func Open(environment Environment) (*Client, error) {
	return open(environment, `Global\JapanStockAutotrader.KabuApi.v1`)
}

func open(environment Environment, name string) (*Client, error) {
	client, err := newClient(environment)
	if err != nil {
		return nil, err
	}
	guard, err := instanceguard.Acquire(name)
	if err != nil {
		client.Close()
		return nil, err
	}
	client.guard = guard
	return client, nil
}

// newClient is also used by package-local mock HTTP tests without a real gate.
func newClient(environment Environment) (*Client, error) {
	port := "18081"
	switch environment {
	case Test:
	case Production:
		port = "18080"
	default:
		return nil, errors.New("接続環境は test または production を指定してください")
	}
	return &Client{
		base: "http://localhost:" + port + "/kabusapi",
		http: &http.Client{
			Timeout: 10 * time.Second,
			// Do not send API credentials via environment proxies or redirects.
			Transport: &http.Transport{
				Proxy:       nil,
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) Authenticate(ctx context.Context, password string) error {
	if password == "" {
		return errors.New("APIパスワードが空です")
	}
	body, _ := json.Marshal(struct {
		Password string `json:"APIPassword"`
	}{password})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	defer clear(body)
	raw, err := c.request(ctx, http.MethodPost, "/token", body)
	if err != nil {
		return err
	}
	defer clear(raw)
	var result struct {
		ResultCode *int   `json:"ResultCode"`
		Token      string `json:"Token"`
	}
	if json.Unmarshal(raw, &result) != nil || result.ResultCode == nil || *result.ResultCode != 0 || result.Token == "" {
		return errors.New("認証応答が不正です")
	}
	c.token = result.Token
	return nil
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.closed = true
	c.http.CloseIdleConnections()
	if c.guard != nil {
		c.guard.Close()
	}
}

func (c *Client) Board(ctx context.Context, symbol string) (json.RawMessage, error) {
	if err := ValidateSymbol(symbol); err != nil {
		return nil, err
	}
	return c.read(ctx, "/board/"+symbol, false)
}

// TimeAndSales fetches the available history, not an arbitrary date range.
// The shared request owner applies a separate conservative one-second budget.
func (c *Client) TimeAndSales(ctx context.Context, symbol string) (json.RawMessage, error) {
	if err := ValidateSymbol(symbol); err != nil {
		return nil, err
	}
	return c.read(ctx, "/timeandsales/"+symbol, false)
}

func (c *Client) Cash(ctx context.Context) (json.RawMessage, error) {
	return c.read(ctx, "/wallet/cash", false)
}

func (c *Client) Margin(ctx context.Context) (json.RawMessage, error) {
	return c.read(ctx, "/wallet/margin", false)
}

func (c *Client) Positions(ctx context.Context, margin bool) (json.RawMessage, error) {
	product := "1"
	if margin {
		product = "2"
	}
	return c.read(ctx, "/positions?product="+product+"&addinfo=true", true)
}

func (c *Client) read(ctx context.Context, path string, array bool) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		return nil, errors.New("API認証が必要です")
	}
	raw, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	want := byte('{')
	if array {
		want = '['
	}
	if len(trimmed) == 0 || trimmed[0] != want {
		return nil, errors.New("API応答の形式が不正です")
	}
	var businessError struct {
		Code *int `json:"Code"`
	}
	if !array && json.Unmarshal(raw, &businessError) == nil && businessError.Code != nil {
		return nil, fmt.Errorf("APIエラー code=%d", *businessError.Code)
	}
	return raw, nil
}

// This allowlist is enforced immediately before any network operation.
func allowed(method, path string) bool {
	if method == http.MethodPost {
		return path == "/token"
	}
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/wallet/cash", "/wallet/margin", "/positions?product=1&addinfo=true", "/positions?product=2&addinfo=true":
		return true
	}
	for _, prefix := range []string{"/board/", "/timeandsales/"} {
		if len(path) > len(prefix) && path[:len(prefix)] == prefix && ValidateSymbol(path[len(prefix):]) == nil {
			return true
		}
	}
	return false
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	if c.closed {
		return nil, errors.New("API接続は終了しています")
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("取得を中断しました: %w", ctx.Err())
	}
	if !allowed(method, path) {
		return nil, errors.New("読み取り専用APIの許可範囲外です")
	}
	trades := strings.HasPrefix(path, "/timeandsales/")
	next := c.next
	if trades && c.nextTrades.After(next) {
		next = c.nextTrades
	}
	if wait := time.Until(next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("取得を中断しました: %w", ctx.Err())
		case <-timer.C:
		}
	}
	c.next = time.Now().Add(250 * time.Millisecond)
	if trades {
		c.nextTrades = time.Now().Add(time.Second)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("APIリクエストを作成できません")
	}
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodGet {
		req.Header.Set("X-API-KEY", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Transport errors may contain URLs or server-controlled text; never echo them.
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil, fmt.Errorf("取得を中断しました: %w", ctx.Err())
		}
		return nil, errors.New("API通信に失敗しました。起動・ログイン・API設定・接続先を確認してください")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			c.token = ""
		}
		return nil, fmt.Errorf("API HTTP %d（応答本文は記録しません）", resp.StatusCode)
	}
	limit := int64(8 << 20)
	if trades {
		limit = 64 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil, fmt.Errorf("取得を中断しました: %w", ctx.Err())
	}
	if err != nil || int64(len(raw)) > limit || !json.Valid(raw) {
		return nil, errors.New("API応答を正常に読み取れません")
	}
	return raw, nil
}
