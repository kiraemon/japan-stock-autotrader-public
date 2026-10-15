package kabu

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newClient(Test)
	if err != nil {
		t.Fatal(err)
	}
	client.base = server.URL + "/kabusapi"
	t.Cleanup(client.Close)
	return client
}

func TestConcurrentReadsShareOneSendBudget(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	client.token = "test-only-token"
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			if _, err := client.Cash(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 4 {
		t.Fatalf("requests=%d", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < 225*time.Millisecond {
			t.Fatal("concurrent requests bypassed budget")
		}
	}
}

func TestReadOnlyBoundary(t *testing.T) {
	for _, path := range []string{"/sendorder", "/sendorder/future", "/cancelorder", "/register", "/unregister/all", "/board/7203@1/../sendorder", "/positions?product=0", "/token?extra=1"} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
			if allowed(method, path) {
				t.Errorf("allowed unsafe request %s %s", method, path)
			}
		}
	}
	client := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unsafe request reached network") })
	if _, err := client.request(context.Background(), "POST", "/sendorder", nil); err == nil {
		t.Fatal("expected denial")
	}
}

func TestAuthenticationAndReads(t *testing.T) {
	var calls []string
	var starts []time.Time
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		starts = append(starts, time.Now())
		if r.URL.Path == "/kabusapi/token" {
			if r.Method != "POST" || r.Header.Get("X-API-KEY") != "" {
				t.Error("bad authentication headers/method")
			}
			fmt.Fprint(w, `{"ResultCode":0,"Token":"test-only-token"}`)
			return
		}
		if r.Header.Get("X-API-KEY") != "test-only-token" {
			t.Error("missing token")
		}
		if strings.HasSuffix(r.URL.Path, "positions") {
			fmt.Fprint(w, `[]`)
		} else {
			fmt.Fprint(w, `{"CurrentPrice":null}`)
		}
	})
	ctx := context.Background()
	if _, err := client.Cash(ctx); err == nil {
		t.Fatal("unauthenticated read succeeded")
	}
	if err := client.Authenticate(ctx, "test-only-password"); err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { _, err := client.Cash(ctx); return err },
		func() error { _, err := client.Margin(ctx); return err },
		func() error { _, err := client.Positions(ctx, false); return err },
		func() error { _, err := client.Positions(ctx, true); return err },
		func() error { _, err := client.Board(ctx, "130A@1"); return err },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 6 {
		t.Fatalf("got %d requests", len(calls))
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < 225*time.Millisecond {
			t.Fatal("rate limit spacing violated")
		}
	}
}

func TestErrorsNeverEchoResponse(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{401, `{"Token":"do-not-log-this","Message":"do-not-log-this"}`},
		{429, `do-not-log-this`},
		{200, `do-not-log-this`},
		{200, `{"Code":4001001,"Message":"do-not-log-this"}`},
		{200, `null`},
		{200, `[]`},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			count := 0
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			client.token = "test-only-token"
			_, err := client.Cash(context.Background())
			if err == nil || strings.Contains(err.Error(), "do-not-log-this") {
				t.Fatalf("unsafe error: %v", err)
			}
			if count != 1 {
				t.Fatal("unexpected retry")
			}
			if tc.status == 401 && client.token != "" {
				t.Fatal("expired token retained")
			}
		})
	}
}

func TestRejectRedirect(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect leaked credentials") }))
	defer destination.Close()
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) })
	if err := client.Authenticate(context.Background(), "test-only-password"); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestCancellationAndEnvironment(t *testing.T) {
	if _, err := newClient("https://example.com"); err == nil {
		t.Fatal("external address accepted")
	}
	client := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("canceled request reached server") })
	client.token = "test-only-token"
	client.next = time.Now().Add(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Cash(ctx); err == nil {
		t.Fatal("cancellation ignored")
	}
	for _, symbol := range []string{"7203@9", "7203@27", "../token", "7203@1?x=y", ""} {
		if ValidateSymbol(symbol) == nil {
			t.Errorf("bad symbol accepted: %s", symbol)
		}
	}
}
