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
	"sync"
	"time"
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
	mu    sync.Mutex
	base  string
	http  *http.Client
	token string
	next  time.Time
}

func New(environment Environment) (*Client, error) {
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
	c.http.CloseIdleConnections()
}

func (c *Client) Board(ctx context.Context, symbol string) (json.RawMessage, error) {
	if err := ValidateSymbol(symbol); err != nil {
		return nil, err
	}
	return c.read(ctx, "/board/"+symbol, false)
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
	const prefix = "/board/"
	return len(path) > len(prefix) && path[:len(prefix)] == prefix && ValidateSymbol(path[len(prefix):]) == nil
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	if !allowed(method, path) {
		return nil, errors.New("読み取り専用APIの許可範囲外です")
	}
	if wait := time.Until(c.next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, errors.New("取得を中断しました")
		case <-timer.C:
		}
	}
	c.next = time.Now().Add(250 * time.Millisecond)
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
		return nil, errors.New("API通信に失敗しました。起動・ログイン・API設定・接続先を確認してください")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			c.token = ""
		}
		return nil, fmt.Errorf("API HTTP %d（応答本文は記録しません）", resp.StatusCode)
	}
	const limit = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit || !json.Valid(raw) {
		return nil, errors.New("API応答を正常に読み取れません")
	}
	return raw, nil
}
