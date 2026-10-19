package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrimarySelectionRejectsDuplicateAndCommonReplacement(t *testing.T) {
	s := study{Phase: "validation"}
	for _, m := range models {
		s.Books = append(s.Books, bookRef{Model: m, Phase: s.Phase, Cohort: "native", Summary: book{Scenario: scenario{BPS: 10, Commission: "sor_conditional_zero"}}})
	}
	if b, e := primaryBooks(s); e != nil || len(b) != 3 {
		t.Fatal("valid primary selection rejected")
	}
	s.Books = append(s.Books, s.Books[0])
	if _, e := primaryBooks(s); e == nil {
		t.Fatal("duplicate primary admitted")
	}
	s.Books = s.Books[:3]
	s.Books[1].Summary.Scenario.Common = true
	if _, e := primaryBooks(s); e == nil {
		t.Fatal("common book silently replaced all candidates")
	}
}
func TestPinnedInputTamperingAndSafeErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "input.json")
	body := []byte(`{"version":1}`)
	if e := os.WriteFile(p, body, 0600); e != nil {
		t.Fatal(e)
	}
	var v any
	if e := readPin(pin{p, digest(body)}, &v); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte("private-marker"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := readPin(pin{p, digest(body)}, &v); e == nil || strings.Contains(e.Error(), "private-marker") {
		t.Fatal("tampered source accepted or leaked")
	}
	for _, s := range []string{"1e9999999", "1/0", strings.Repeat("1", 257)} {
		if _, e := exact(s); e == nil {
			t.Fatal("unbounded monetary string accepted")
		}
	}
}
func TestOutputPreservesExistingEvidenceAndRejectsOtherDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	if e := os.MkdirAll("data/research", 0700); e != nil {
		t.Fatal(e)
	}
	p := "data/research/result.json"
	if e := saveNew(p, map[string]int{"value": 1}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(p)
	if e := saveNew(p, map[string]int{"value": 2}); e == nil {
		t.Fatal("existing evidence overwritten")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("existing evidence changed")
	}
	for _, p := range []string{"data/research/../../outside.json", "logs/result.json", filepath.Join(t.TempDir(), "out.json")} {
		if e := saveNew(p, nil); e == nil {
			t.Fatal("output escaped fixed research directory")
		}
	}
}
func TestHelpAndInvalidArgumentsDoNotReadSources(t *testing.T) {
	var w bytes.Buffer
	if e := run(context.Background(), []string{"-h"}, &w); e != nil || !strings.Contains(w.String(), "local files only") {
		t.Fatal("help contract")
	}
	if e := run(context.Background(), nil, &bytes.Buffer{}); e == nil {
		t.Fatal("missing input accepted")
	}
}
func TestCashflowReconciliationKeepsSkippedCandidatesAndMissingCounts(t *testing.T) {
	t.Chdir(t.TempDir())
	e, _ := pathFixture()
	e.Costs = costs{Gross: "-1", Net: "-2", Budget: "300001"}
	e.Trade.Exit = 9900
	e.Trade.EntryAt = clock(e.Date, 540)
	e.Trade.ExitAt = clock(e.Date, 542)
	b := book{Version: "ordinary-margin-research-book-v0.1", Scenario: scenario{BPS: 10, Commission: "sor_conditional_zero"}, Cases: 30, Unevaluable: 20, Candidates: 2, Executed: 1, Gross: "-1", Net: "-2", After: "-12", Data: "10", Ledger: []entry{e}}
	skipped := e
	skipped.Symbol = "0001@1"
	skipped.Rank = 2
	skipped.Status = "skipped_busy"
	b.Ledger = append(b.Ledger, skipped)
	raw, _ := json.Marshal(b)
	if er := os.WriteFile("book.json", raw, 0600); er != nil {
		t.Fatal(er)
	}
	r := bookRef{Model: models[0], Artifact: pin{"book.json", digest(raw)}, Summary: b}
	a, _, er := prepareAnalysis(study{Phase: "validation", Dates: []string{e.Date}}, pin{}, r)
	if er != nil || a.Executed != 1 || a.Unevaluable != 20 || a.Groups["all"].Count != 2 || a.Groups["status/skipped_busy"].Count != 1 || a.Daily[e.Date] != "-2" {
		t.Fatal("skipped or unavailable cases changed realized cashflow", er)
	}
}
