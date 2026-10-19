// Package architecture_test checks source boundaries, not runtime behaviour.
// Test fixtures stay in memory; no market data, credentials, or API is accessed.
package architecture_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const internalPrefix = "github.com/kiraemon/japan-stock-autotrader/internal/"

func feature(name string) string {
	if !strings.HasPrefix(name, "features/") {
		return ""
	}
	return strings.Split(strings.TrimPrefix(name, "features/"), "/")[0]
}

func within(name, parent string) bool { return name == parent || strings.HasPrefix(name, parent+"/") }

func restrictedCore(name string) bool {
	if within(name, "strategy") || within(name, "marketdata") {
		return true
	}
	switch feature(name) {
	case "backtest", "backtests01", "researchbook", "buildbars", "comparebars", "papertrade", "replaysignals", "streamsignals", "streambars", "assesscapital", "reviewstrategies", "prepareorb", "prepareuniverse", "observespread":
		return true
	}
	return false
}

func importViolation(source, imported string) string {
	if strings.HasPrefix(imported, internalPrefix) {
		target := strings.TrimPrefix(imported, internalPrefix)
		if a, b := feature(source), feature(target); a != "" && b != "" && a != b {
			return "スライス間の直接依存です。cmdで組み立てるか、必要な小さな共有契約を定義してください"
		}
		if restrictedCore(source) {
			if within(target, "marketdata") {
				return ""
			}
			if !within(source, "marketdata") && within(target, "strategy") {
				return ""
			}
			if own := feature(source); own != "" && own == feature(target) {
				return ""
			}
			return "計算・再生側から外部接続/保存基盤への依存です。入出力は呼出側で組み立ててください"
		}
	}
	if restrictedCore(source) {
		for _, external := range []string{"net", "os", "syscall", "database/sql", "plugin"} {
			if within(imported, external) {
				return "計算・再生側による通信/ファイル等への直接依存です。値や小さな入力契約を受け取ってください"
			}
		}
	}
	return ""
}

// Inspect every internal production file, including all OS/build-tag variants.
// Test-only imports are intentionally exempt so independent fixture/oracle
// comparisons can compose features without changing production dependencies.
func checkBoundaries(files fs.FS) ([]string, error) {
	violations := []string{}
	count := 0
	err := fs.WalkDir(files, "internal", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, raw, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("%s: importの構文を確認してください", name)
		}
		count++
		source := strings.TrimPrefix(path.Dir(name), "internal/")
		for _, item := range parsed.Imports {
			imported, err := strconv.Unquote(item.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: importの形式を確認してください", name)
			}
			if reason := importViolation(source, imported); reason != "" {
				violations = append(violations, fmt.Sprintf("%s -> %s: %s", name, imported, reason))
			}
		}
		return nil
	})
	if err == nil && count == 0 {
		err = fmt.Errorf("検査対象の本体Goファイルがありません")
	}
	return violations, err
}

func TestProductionImportBoundaries(t *testing.T) {
	violations, err := checkBoundaries(os.DirFS("../.."))
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}

func TestImportBoundaryChecksRejectViolations(t *testing.T) {
	for _, c := range []struct {
		name, file, imported string
	}{
		{"feature_to_feature", "features/backtest/run.go", internalPrefix + "features/buildbars"},
		{"strategy_to_broker", "strategy/r001/rules.go", internalPrefix + "kabu"},
		{"paper_to_network", "features/papertrade/send.go", "net/http"},
		{"history_candidates_to_files", "features/streambars/feed.go", "os"},
		{"orb_inputs_to_network", "features/prepareorb/opening.go", "net/http"},
		{"s01_replay_to_files", "features/backtests01/run.go", "os"},
		{"data_to_files", "marketdata/bars.go", "os"},
		{"windows_file_also_checked", "strategy/r001/clock_windows.go", "syscall"},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := fstest.MapFS{"internal/" + c.file: {Data: []byte("package fixture\nimport _ " + strconv.Quote(c.imported))}}
			violations, err := checkBoundaries(files)
			if err != nil || len(violations) != 1 {
				t.Fatalf("違反例が検出されません: count=%d err=%v", len(violations), err)
			}
		})
	}
}

func TestImportBoundaryChecksAllowCompositionAndRejectEmptyScan(t *testing.T) {
	files := fstest.MapFS{
		"internal/features/papertrade/replay.go":      {Data: []byte("package fixture\nimport _ " + strconv.Quote(internalPrefix+"strategy/r001"))},
		"internal/strategy/r001/rules.go":             {Data: []byte("package fixture\nimport _ " + strconv.Quote(internalPrefix+"marketdata"))},
		"internal/features/papertrade/replay_test.go": {Data: []byte("package fixture\nimport _ " + strconv.Quote(internalPrefix+"features/backtest"))},
		"internal/features/dashboard/server.go":       {Data: []byte("package fixture\nimport _ \"net/http\"")},
		"internal/features/collectquotes/collect.go":  {Data: []byte("package fixture\nimport _ " + strconv.Quote(internalPrefix+"kabu"))},
	}
	if violations, err := checkBoundaries(files); err != nil || len(violations) != 0 {
		t.Fatalf("許可された依存が拒否されました: %v %v", violations, err)
	}
	if _, err := checkBoundaries(fstest.MapFS{"internal/only_test.go": {Data: []byte("package fixture")}}); err == nil {
		t.Fatal("本体ファイルなしの検査が成功しました")
	}
}
