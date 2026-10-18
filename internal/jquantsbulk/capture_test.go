package jquantsbulk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureClient(t *testing.T, status int) (*client, *int) {
	t.Helper()
	calls := 0
	r := &Report{Month: "2026-06", Files: []File{}}
	c := &client{key: "synthetic-key", month: r.Month, interval: time.Millisecond, report: r, keys: map[string]bool{}}
	c.http = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body := ""
		if req.URL.Host == "api.jquants.com" {
			if req.Header.Get("x-api-key") != "synthetic-key" {
				t.Error("missing API authentication")
			}
			switch req.URL.Path {
			case "/v2/bulk/list":
				body = `{"data":[{"Key":"equities/bars/minute/historical/2026/minute_202606.csv.gz","Size":"3.0","LastModified":"2026-07-01T00:00:00Z"}]}`
			case "/v2/bulk/get":
				body = `{"url":"https://` + storageHost + `/file.csv.gz?signature=synthetic-secret"}`
			default:
				t.Error("unexpected API path")
			}
		} else {
			if req.Header.Get("x-api-key") != "" {
				t.Error("API key leaked to object storage")
			}
			body = "abc"
		}
		return &http.Response{StatusCode: status, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return c, &calls
}

func TestBulkDownloadProvenanceAndNoCredentialForwarding(t *testing.T) {
	c, calls := fixtureClient(t, 200)
	dir := t.TempDir()
	if err := c.capture(context.Background(), dir, false); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 || c.report.Requests != 2 || len(c.report.Files) != 1 {
		t.Fatal("wrong request count")
	}
	f := c.report.Files[0]
	raw, err := os.ReadFile(filepath.Join(dir, f.Local))
	h := sha256.Sum256(raw)
	if err != nil || string(raw) != "abc" || f.SHA256 != hex.EncodeToString(h[:]) {
		t.Fatal("download hash mismatch")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary file retained")
	}
}

func TestTruncatedBulkDownloadNeverBecomesCompleteFile(t *testing.T) {
	c, _ := fixtureClient(t, 200)
	original := c.http.Transport
	c.http.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := original.RoundTrip(req)
		if req.URL.Host != "api.jquants.com" {
			resp.Body = io.NopCloser(strings.NewReader("ab"))
			resp.ContentLength = 3
		}
		return resp, err
	})
	dir := t.TempDir()
	if err := c.capture(context.Background(), dir, false); err == nil {
		t.Fatal("truncated file accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 || c.report.Files[0].Local != "" || c.report.Files[0].SHA256 != "" {
		t.Fatal("partial file was finalized")
	}
}

func TestListOnlyAnd429Stop(t *testing.T) {
	c, calls := fixtureClient(t, 200)
	if err := c.capture(context.Background(), t.TempDir(), true); err != nil || *calls != 1 {
		t.Fatal("list-only downloaded data", err)
	}
	c, calls = fixtureClient(t, 429)
	for i := 0; i < 2; i++ {
		_, err := c.metadata(context.Background(), "/v2/bulk/list", url.Values{"endpoint": {Endpoint}, "from": {c.month}, "to": {c.month}})
		if err == nil || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("rate limit or secret disclosure")
		}
	}
	if *calls != 1 {
		t.Fatal("429 automatically retried")
	}
}

func TestBulkAllowlistAndCancellation(t *testing.T) {
	c, calls := fixtureClient(t, 200)
	for _, p := range []struct {
		path string
		q    url.Values
	}{
		{"/v2/sendorder", url.Values{}},
		{"/v2/bulk/get", url.Values{"key": {"unlisted"}}},
		{"/v2/bulk/list", url.Values{"endpoint": {"/fins/summary"}, "from": {c.month}, "to": {c.month}}},
		{"/v2/bulk/list", url.Values{"endpoint": {Endpoint, Endpoint}, "from": {c.month}, "to": {c.month}}},
	} {
		if _, err := c.metadata(context.Background(), p.path, p.q); err == nil {
			t.Fatal("disallowed request")
		}
	}
	if *calls != 0 {
		t.Fatal("invalid request reached API")
	}
	c.last = time.Now()
	c.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.metadata(ctx, "/v2/bulk/list", url.Values{"endpoint": {Endpoint}, "from": {c.month}, "to": {c.month}}); err == nil || *calls != 0 {
		t.Fatal("cancellation ignored")
	}
}

func TestSignedURLAndKeysRefuseOtherDestinations(t *testing.T) {
	for _, raw := range []string{"http://synthetic.s3.ap-northeast-1.amazonaws.com/a?b=c", "https://example.com/a?b=c", "https://127.0.0.1/a?b=c", "https://synthetic.s3.ap-northeast-1.amazonaws.com.evil/a?b=c", "https://user@synthetic.s3.ap-northeast-1.amazonaws.com/a?b=c", "https://synthetic.s3.ap-northeast-1.amazonaws.com:443/a?b=c"} {
		if _, err := signedURL(raw); err == nil {
			t.Fatal("URL accepted")
		}
	}
	for _, key := range []string{"equities/bars/minute/../202606.csv.gz", "equities/trades/202606.csv.gz", "equities/bars/minute/202605.csv.gz"} {
		if validKey(key, "2026-06") {
			t.Fatal("key accepted")
		}
	}
	if ValidateMonth("2024-10") == nil || ValidateMonth("2026-6") == nil {
		t.Fatal("month validation")
	}
}

func TestSizeIsExactAcrossJSONNumberForms(t *testing.T) {
	for _, value := range []string{"3", "3.0", "3e0", "30e-1"} {
		if n, ok := sizeBytes(json.Number(value)); !ok || n != 3 {
			t.Fatal("exact integer rejected")
		}
	}
	for _, value := range []string{"3.5", "1e99", "", "not-a-number"} {
		if _, ok := sizeBytes(json.Number(value)); ok {
			t.Fatal("inexact/out-of-range size accepted")
		}
	}
}
