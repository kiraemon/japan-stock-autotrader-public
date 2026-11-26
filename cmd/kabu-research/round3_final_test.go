package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRound3FreezeRequiresCompleteSyntheticProvenance(t *testing.T) {
	t.Chdir(t.TempDir())
	write := func(t *testing.T, path string, raw []byte) studyArtifact {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return studyArtifact{Path: path, SHA: hash(raw)}
	}
	writeJSON := func(t *testing.T, path string, value any) studyArtifact {
		t.Helper()
		return studyArtifact{Path: path, SHA: hash(writeS01FixtureJSON(t, path, value))}
	}
	manifest := []byte("synthetic manifest")
	revision := strings.Repeat("a", 40)
	matrix := write(t, "synthetic-matrix.json", []byte("{}"))
	book := write(t, "synthetic-book.json", []byte("{}"))
	report := round3StudyReport{
		Version: round3Version, Phase: "validation", NewPNL: true, ManifestSHA: hash(manifest),
		ScopeSHA: round3ScopeSHA, PeriodSHA: round3PeriodSHA, Sessions: make([]string, 58),
		Matrices: make([]studyArtifact, 44), Books: make([]studyBook, round3BookCount),
	}
	for i := range report.Matrices {
		report.Matrices[i] = matrix
	}
	for i := range report.Books {
		report.Books[i] = studyBook{Artifact: book}
	}
	reportPin := writeJSON(t, "validation.json", report)
	record := map[string]any{
		"command": "study-round3", "code_revision": revision, "working_tree_at_build": "false",
		"report_sha256": reportPin.SHA, "input_sha256": hash(manifest),
	}
	audit := map[string]any{
		"report_sha256": reportPin.SHA, "independent_decimal_accounting": true,
		"independent_price_and_indicators": true, "holdout_pnl_read": false, "books": round3BookCount, "price_cases": 58 * 30 * 3,
	}
	freeze := studyFreeze{
		Version: 1, Revision: revision, OriginalRevision: revision, ManifestSHA: hash(manifest), PlanSHA: round3ScopeSHA,
		Development: reportPin, DevelopmentRecord: writeJSON(t, "record.json", record),
		IndependentAudit: writeJSON(t, "audit.json", audit), Verification: write(t, "verify.log", []byte("RESULT=PASS\n")),
		CoreSources: []studyArtifact{
			write(t, "internal/model.go", []byte("package model\n")),
			write(t, "cmd/kabu-research/main.go", []byte("package main\n")),
		},
	}
	write(t, "internal/model_test.go", []byte("package model\n"))
	require := func(t *testing.T, input studyFreeze, want string) {
		t.Helper()
		ref := writeJSON(t, "freeze.json", input)
		got, err := checkRound3FreezeIdentity(ref.Path, manifest, revision, "false")
		if want == "" {
			if err != nil || got != ref.SHA {
				t.Fatalf("complete evidence did not produce the frozen hash: %q, %v", got, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), want) || got != "" {
			t.Fatalf("expected refusal %q, received %q and %v", want, got, err)
		}
	}
	t.Run("valid evidence", func(t *testing.T) { require(t, freeze, "") })
	t.Run("audit covered a sealed period", func(t *testing.T) {
		input := freeze
		audit["holdout_pnl_read"] = true
		input.IndependentAudit = writeJSON(t, "leaky-audit.json", audit)
		require(t, input, "独立検算を固定")
	})
	t.Run("failed verification", func(t *testing.T) {
		input := freeze
		input.Verification = write(t, "failed.log", []byte("RESULT=FAIL\n"))
		require(t, input, "独立検算を固定")
	})
	t.Run("changed book bytes", func(t *testing.T) {
		write(t, book.Path, []byte("[]"))
		t.Cleanup(func() { write(t, book.Path, []byte("{}")) })
		require(t, freeze, "hash")
	})
	t.Run("new unpinned source", func(t *testing.T) {
		write(t, "internal/added.go", []byte("package model\n"))
		require(t, freeze, "未固定")
	})
}
