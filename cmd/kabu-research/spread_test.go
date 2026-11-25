package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/observespread"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

func spreadFixture(t *testing.T) spreadManifest {
	t.Helper()
	at := marketdata.Clock("2026-10-16", 9, 16)
	board := syntheticBoard(at, 100) // fabricated fixture, normalized as observed solely for testing
	raw := writeS01FixtureJSON(t, "snapshot.json", map[string]any{"schema_version": 1, "environment": "production", "complete": true, "records": []any{map[string]any{"endpoint": "/wallet/cash", "data": map[string]string{"fixture": "private-account-marker"}}, map[string]any{"endpoint": "/board/0000@1", "received_at": at, "data": json.RawMessage(board)}}})
	m := spreadManifest{Version: 1, Inspection: at.AddDate(0, 0, 1), Files: []studyArtifact{{"snapshot.json", hash(raw)}}}
	writeS01FixtureJSON(t, "manifest.json", m)
	return m
}

func TestSpreadCLIReportsReceiptTimeWithoutAccountValuesAndRepeatsExactly(t *testing.T) {
	t.Chdir(t.TempDir())
	spreadFixture(t)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"quote-spread-check", "-input", "manifest.json"}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "samples=1 / structurally_valid=1 / fresh_at_receipt_2s=1") || strings.Contains(out.String(), "private-account-marker") {
			t.Fatal("summary incorrect or leaked")
		}
	}
	paths, _ := filepath.Glob("data/research/quote-spread-observation-*.json")
	if len(paths) != 2 {
		t.Fatal("reports missing")
	}
	a, _ := os.ReadFile(paths[0])
	b, _ := os.ReadFile(paths[1])
	if !bytes.Equal(a, b) || bytes.Contains(a, []byte("private-account-marker")) {
		t.Fatal("repeat changed or account record copied")
	}
	var r observespread.Report
	if json.Unmarshal(a, &r) != nil || r.ForwardTest || r.ExecutionIncluded || r.Trading || r.Quality {
		t.Fatal("observation promoted")
	}
}

func TestSpreadCLIRejectsTamperDuplicateSourcesAndTestArchive(t *testing.T) {
	for _, fault := range []string{"hash", "duplicate", "copied_file", "test", "future", "unknown"} {
		t.Run(fault, func(t *testing.T) {
			t.Chdir(t.TempDir())
			m := spreadFixture(t)
			switch fault {
			case "hash":
				m.Files[0].SHA = strings.Repeat("0", 64)
			case "duplicate":
				m.Files = append(m.Files, m.Files[0])
			case "copied_file":
				raw, err := os.ReadFile("snapshot.json")
				if err != nil || os.WriteFile("copy.json", raw, 0600) != nil {
					t.Fatal("fixture copy")
				}
				m.Files = append(m.Files, studyArtifact{"copy.json", m.Files[0].SHA})
			case "test":
				raw, _ := os.ReadFile("snapshot.json")
				raw = bytes.ReplaceAll(raw, []byte(`"production"`), []byte(`"test"`))
				if os.WriteFile("snapshot.json", raw, 0600) != nil {
					t.Fatal("write")
				}
				m.Files[0].SHA = hash(raw)
			case "future":
				m.Inspection = m.Inspection.AddDate(100, 0, 0)
			case "unknown":
				m.Version = 2
			}
			writeS01FixtureJSON(t, "manifest.json", m)
			err := run(context.Background(), []string{"quote-spread-check", "-input", "manifest.json"}, &bytes.Buffer{})
			if err == nil || strings.Contains(err.Error(), "private-account-marker") {
				t.Fatal("unsafe input admitted or leaked")
			}
		})
	}
}
