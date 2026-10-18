package jquantscli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testBridge(t *testing.T, status int) (*bridge, *int) {
	t.Helper()
	calls := 0
	b := &bridge{key: "synthetic-real-key", nonce: "synthetic-nonce", jobs: []Job{{"0000", "2026-06-01", "2026-06-02"}}, interval: 20 * time.Millisecond, budget: 3}
	b.client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.jquants.com" || r.URL.Scheme != "https" || r.Header.Get("x-api-key") != "synthetic-real-key" {
			t.Error("wrong upstream or credential")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"data":[],"message":"synthetic-response-secret"}`)), Header: http.Header{}}, nil
	})}
	return b, &calls
}

func request(b *bridge, method, path, nonce string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("x-api-key", nonce)
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}

func TestBridgeAllowlistIncludesPaginationAndRateBudget(t *testing.T) {
	b, calls := testBridge(t, 200)
	path := "/v2/equities/bars/minute?code=0000&from=2026-06-01&to=2026-06-02"
	for _, c := range []struct{ method, path, key string }{
		{"POST", path, b.nonce}, {"GET", path, "bad"}, {"GET", "/v2/cli/api-key", b.nonce}, {"GET", path + "&date=2026-06-01", b.nonce}, {"GET", path + "&code=1111", b.nonce}, {"GET", strings.Replace(path, "0000", "1111", 1), b.nonce},
	} {
		if request(b, c.method, c.path, c.key).Code < 400 {
			t.Fatal("disallowed request admitted")
		}
	}
	if *calls != 0 {
		t.Fatal("invalid request reached upstream")
	}
	started := time.Now()
	for i := 0; i < 3; i++ {
		if request(b, "GET", path+"&pagination_key=next", b.nonce).Code != 200 {
			t.Fatal("pagination refused")
		}
	}
	if time.Since(started) < 35*time.Millisecond || *calls != 3 {
		t.Fatal("request spacing not enforced")
	}
	if request(b, "GET", path, b.nonce).Code != 429 || *calls != 3 {
		t.Fatal("budget exceeded")
	}
}

func TestBridgeStopsOn429WithoutEchoingBody(t *testing.T) {
	b, calls := testBridge(t, 429)
	path := "/v2/equities/bars/minute?code=0000&from=2026-06-01&to=2026-06-02"
	for i := 0; i < 2; i++ {
		w := request(b, "GET", path, b.nonce)
		if w.Code != 429 || strings.Contains(w.Body.String(), "synthetic-") {
			t.Fatal("status or body disclosure")
		}
	}
	if *calls != 1 {
		t.Fatal("retried a rate-limited request")
	}
}

func TestCLIEnvironmentIsolationAndInvalidJobs(t *testing.T) {
	values := childEnvironment([]string{"PATH=normal", "OP_SERVICE_ACCOUNT_TOKEN=secret", "JQUANTS_API_KEY=secret", "JQUANTS_BASE_URL=https://bad", "HTTPS_PROXY=bad"}, "nonce", "http://127.0.0.1:1/v2")
	joined := strings.Join(values, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "bad") || !strings.Contains(joined, "JQUANTS_API_KEY=nonce") {
		t.Fatal("child inherited credentials or destination")
	}
	for _, jobs := range [][]Job{nil, {{"0000", "2026-06-02", "2026-06-01"}}, {{"0000", "2026-01-01", "2026-06-01"}}, {{"0000", "2026-06-01", "2026-06-02"}, {"0000", "2026-06-01", "2026-06-02"}}} {
		if ValidateJobs(jobs) == nil {
			t.Fatal("invalid job admitted")
		}
	}
	if _, _, err := Capture(context.Background(), "unused", "", []Job{{"0000", "2026-06-01", "2026-06-02"}}); err == nil {
		t.Fatal("empty key admitted")
	}
}
