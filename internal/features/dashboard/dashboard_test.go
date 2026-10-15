package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func saveSynthetic(t *testing.T, root, name string, at time.Time, price any, apiError string) []byte {
	t.Helper()
	value := map[string]any{"schema_version": 1, "environment": "production", "started_at": at.Add(-time.Second), "finished_at": at, "complete": apiError == "", "records": []any{
		map[string]any{"endpoint": "/wallet/cash", "received_at": at, "data": map[string]any{"StockAccountWallet": 987654321, "private_marker": "secret-account-marker"}},
		map[string]any{"endpoint": "/board/7203@1", "received_at": at, "error": apiError, "data": map[string]any{"SymbolName": "<script>unsafe()</script>", "CurrentPrice": price, "CurrentPriceTime": at.Add(-time.Second)}},
	}}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "production"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "production", name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadChronologyRedactionAndMissingLatest(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	before := saveSynthetic(t, root, "snapshot-z.json", now.Add(-10*time.Second), 1234, "")
	saveSynthetic(t, root, "snapshot-a.json", now, nil, "secret-error-marker")
	if err := os.WriteFile(filepath.Join(root, "production", "snapshot-corrupt.json"), []byte(`{"password":"secret-account-marker"`), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := Read(context.Background(), root, "production", now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Files != 3 || v.InvalidFiles != 1 || v.CompleteFiles != 1 || v.IncompleteFiles != 1 || len(v.Quotes) != 1 || len(v.Recent) != 2 || !v.LatestAt.Equal(now) {
		t.Fatalf("metadata mismatch: %+v", v)
	}
	q := v.Quotes[0]
	if q.Price != nil || q.MarketTime != nil || !q.ReceivedAt.Equal(now) || len(q.Points) != 1 || q.Points[0].Price != 1234 {
		t.Fatal("latest failed observation must not reuse old price")
	}
	if v.Recent[0].Records != 2 || v.Recent[0].Complete {
		t.Fatal("record count/chronology mismatch")
	}
	raw, _ := json.Marshal(v)
	for _, forbidden := range []string{"987654321", "StockAccountWallet", "secret-account-marker", "secret-error-marker", "/wallet/"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("private source data escaped projection")
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, "production", "snapshot-z.json"))
	if string(before) != string(after) {
		t.Fatal("source changed")
	}
	if _, err := Read(context.Background(), root, "test", now); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, root, "production", now); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestReadKeepsLast60PointsAndNullDoesNotBecomeZero(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	for i := 0; i < 62; i++ {
		saveSynthetic(t, root, "snapshot-"+time.Duration(i).String()+".json", now.Add(time.Duration(i)*time.Second), 100+i, "")
	}
	v, err := Read(context.Background(), root, "production", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Quotes[0].Points) != 60 || v.Quotes[0].Points[0].Price != 102 || len(v.Recent) != 6 {
		t.Fatal("history bound failed")
	}
	saveSynthetic(t, root, "snapshot-null.json", now.Add(100*time.Second), nil, "")
	v, err = Read(context.Background(), root, "production", now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Quotes[0].Price != nil {
		t.Fatal("null became a price")
	}
}

func TestHandlerBoundariesAndCache(t *testing.T) {
	calls := map[string]int{}
	handler := Handler(18700, func(_ context.Context, env string) (View, error) {
		calls[env]++
		return Demo(env, time.Now().UTC()), nil
	}, nil)
	tests := []struct {
		method, path, host, origin, site string
		status                           int
	}{
		{"GET", "/", "127.0.0.1:18700", "", "", 200},
		{"GET", "/api/status", "127.0.0.1:18700", "", "same-origin", 200},
		{"GET", "/api/status?environment=test", "localhost:18700", "http://localhost:18700", "same-origin", 200},
		{"GET", "/api/status?environment=wrong", "127.0.0.1:18700", "", "", 400},
		{"POST", "/api/status", "127.0.0.1:18700", "", "", 405},
		{"GET", "/", "evil.example", "", "", 403},
		{"GET", "/", "127.0.0.1:18700", "https://evil.example", "", 403},
		{"GET", "/", "127.0.0.1:18700", "", "cross-site", 403},
		{"GET", "/data/production/snapshot.json", "127.0.0.1:18700", "", "", 404},
		{"GET", "/secrets/onepassword.json", "127.0.0.1:18700", "", "", 404},
		{"GET", "/assets/dashboard.js", "127.0.0.1:18700", "", "", 200},
		{"GET", "/assets/dashboard.css", "127.0.0.1:18700", "", "", 200},
		{"HEAD", "/", "127.0.0.1:18700", "", "", 200},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, nil)
		r.Header.Set("Origin", tt.origin)
		r.Header.Set("Sec-Fetch-Site", tt.site)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Fatalf("%s %s: %d", tt.method, tt.path, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing browser protections")
		}
		if tt.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
	if calls["production"] != 1 || calls["test"] != 1 {
		t.Fatal("requests bypassed per-environment cache")
	}
}

func TestFailedLoadDoesNotExposeErrorOrOldView(t *testing.T) {
	h := Handler(18700, func(context.Context, string) (View, error) { return View{}, os.ErrPermission }, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18700/api/status", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), os.ErrPermission.Error()) {
		t.Fatal("load failure not safely reported")
	}
}

func TestHeartbeatBypassesSnapshotCache(t *testing.T) {
	loads, checks := 0, 0
	h := Handler(18700, func(context.Context, string) (View, error) { loads++; return Demo("test", time.Now()), nil }, func(string) Runtime {
		checks++
		state := "active"
		if checks > 1 {
			state = "stale"
		}
		return Runtime{State: state, Phase: "waiting"}
	})
	for _, expected := range []string{"active", "stale"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:18700/api/status?environment=test", nil))
		var v View
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Runtime.State != expected {
			t.Fatal("heartbeat classification was cached")
		}
	}
	if loads != 1 || checks != 2 {
		t.Fatal("cache boundaries changed")
	}
}
