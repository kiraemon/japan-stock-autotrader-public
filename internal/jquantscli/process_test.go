package jquantscli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const syntheticCLI = `package main
import ("fmt"; "os"; "strings")
func main() {
  args := os.Args[1:]
  nonce := os.Getenv("JQUANTS_API_KEY")
  if len(args) != 10 || strings.Join(args[:5], " ") != "--output json eq minute --code" ||
    strings.Join(args[6:], " ") != "--from 2026-06-01 --to 2026-06-02" ||
    len(nonce) != 48 || os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") != "" ||
    !strings.HasPrefix(os.Getenv("JQUANTS_BASE_URL"), "http://127.0.0.1:") {
    os.Exit(8)
  }
  switch args[5] {
  case "0001": fmt.Print("null")
  case "0002": fmt.Fprint(os.Stderr, "synthetic diagnostic detail"); os.Exit(9)
  default: fmt.Printf("[{\"Code\":%q}]", args[5])
  }
}`

func TestCaptureRunsIsolatedCLIAndKeepsOnlyValidResults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "synthetic-token")
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(syntheticCLI), 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "mock-cli.exe")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", exe, src)
	build.Dir = dir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile mock CLI: %v: %s", err, output)
	}
	first := Job{"0000", "2026-06-01", "2026-06-02"}
	for _, tc := range []struct{ code, message string }{
		{"0003", ""}, {"0001", "JSON配列"}, {"0002", "公式CLI取得に失敗"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			second := Job{tc.code, first.From, first.To}
			results, stats, err := Capture(ctx, exe, "synthetic-key", []Job{first, second})
			if stats.Requests != 0 || stats.IntervalMS != 1250 || len(results) == 0 ||
				results[0].Code != first.Code || string(results[0].Data) != `[{"Code":"0000"}]` ||
				results[0].Captured.Before(results[0].Started) {
				t.Fatalf("first local result or bridge stats changed: %v, %v", stats, err)
			}
			if tc.message == "" {
				if err != nil || len(results) != 2 || results[1].Code != tc.code {
					t.Fatalf("valid CLI results not returned in order: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.message) ||
				strings.Contains(err.Error(), "synthetic diagnostic detail") || len(results) != 1 {
				t.Fatalf("invalid result was appended or diagnostic leaked: %v", err)
			}
		})
	}
}
