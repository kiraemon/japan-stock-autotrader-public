package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/dashboard"
)

func TestArguments(t *testing.T) {
	for _, args := range [][]string{{"-port", "0"}, {"-port", "180000"}, {"unexpected"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("expected invalid argument")
		}
	}
	if err := run(context.Background(), []string{"-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

type startupMessage chan string

func (message startupMessage) Write(body []byte) (int, error) {
	message <- string(body)
	return len(body), nil
}

func TestDashboardServesDemoAndSavedDataThenStops(t *testing.T) {
	root := t.TempDir()
	testDir := filepath.Join(root, "test")
	if err := os.Mkdir(testDir, 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"schema_version":1,"environment":"test","started_at":"2026-06-22T00:00:00Z",
		"finished_at":"2026-06-22T00:01:00Z","complete":true,"records":[
		{"endpoint":"/board/0000@1","received_at":"2026-06-22T00:00:30Z","data":{"CurrentPrice":100}}]}`
	if err := os.WriteFile(filepath.Join(testDir, "snapshot-synthetic.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"demo", "saved"} {
		t.Run(name, func(t *testing.T) {
			isDemo := name == "demo"
			reservation, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := strconv.Itoa(reservation.Addr().(*net.TCPAddr).Port)
			if err := reservation.Close(); err != nil {
				t.Fatal(err)
			}
			args := []string{"-port", port, "-data-dir", root}
			if isDemo {
				args = append(args, "-demo")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ready := make(startupMessage, 1)
			done := make(chan error, 1)
			go func() { done <- run(ctx, args, ready) }()
			select {
			case message := <-ready:
				if !strings.Contains(message, "http://127.0.0.1:"+port+"/") {
					t.Fatalf("unexpected startup message: %q", message)
				}
			case err := <-done:
				t.Fatalf("dashboard failed to start: %v", err)
			case <-ctx.Done():
				t.Fatal("dashboard never announced readiness")
			}
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			response, err := client.Get("http://127.0.0.1:" + port + "/api/status?environment=test")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var view dashboard.View
			if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || view.Demo != isDemo || view.Environment != "test" {
				t.Fatalf("wrong dashboard mode: status %d, view %+v", response.StatusCode, view)
			}
			if isDemo {
				if view.Runtime.State != "active" || len(view.Quotes) == 0 {
					t.Fatalf("incomplete synthetic dashboard: %+v", view)
				}
			} else if view.Files != 1 || !view.Quality.Available {
				t.Fatalf("saved snapshot quality not projected: %+v", view)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("dashboard did not shut down")
			}
		})
	}
}
