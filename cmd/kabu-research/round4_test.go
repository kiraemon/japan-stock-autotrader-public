package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/prepareorb"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/features/researchbook"
	"github.com/kiraemon/japan-stock-autotrader-public/internal/strategy/round4"
	"strings"
	"testing"
)

func TestRound4ManifestAndCommandCannotOpenNewHoldout(t *testing.T) {
	good := round4Manifest{Version: 1, Plan: studyArtifact{SHA: round4.PlanSHA}, Source: studyArtifact{SHA: round4SourceSHA}, Roots: map[string]studyArtifact{}}
	for phase, digest := range round4RootSHA {
		good.Roots[phase] = studyArtifact{SHA: digest}
	}
	if e := validateRound4Manifest(good); e != nil {
		t.Fatal(e)
	}
	good.Roots["future"] = studyArtifact{SHA: strings.Repeat("0", 64)}
	if e := validateRound4Manifest(good); e == nil {
		t.Fatal("new source admitted")
	}
	for _, args := range [][]string{{"study-round4", "-input", "missing", "-phase", "holdout"}, {"study-round4", "-input", "missing", "-phase", "future"}, {"study-round4", "-input", "missing", "-freeze", "missing"}} {
		if e := run(context.Background(), args, &bytes.Buffer{}); e == nil {
			t.Fatal("unfixed phase admitted")
		}
	}
}
func TestRound4SelectionCannotUseFutureExecutionOrMutateReference(t *testing.T) {
	rows := []round3MatrixRow{}
	ratio := "2"
	for i := 1; i <= 30; i++ {
		rows = append(rows, round3MatrixRow{Rank: i, Opening: prepareorb.Report{CalculationAvailable: true, RelativeVolume: &ratio}, Models: []researchbook.Case{{Date: "2025-11-04", Symbol: fmt.Sprintf("%04d@1", i), Rank: i, Status: "no_trade"}, {}, {}}})
	}
	first, e := selectRound4Day(context.Background(), rows)
	if e != nil {
		t.Fatal(e)
	}
	rows[0].Models[0].Status = "unevaluable"
	rows[29].Models[0].Trade = &researchbook.Trade{EntryPrice: 123}
	second, e := selectRound4Day(context.Background(), rows)
	if e != nil || first[0].Selected != second[0].Selected || second[29].Selected {
		t.Fatal("future outcomes changed ranking")
	}
	c := researchbook.Case{Reasons: []string{"unchanged"}, Trade: &researchbook.Trade{EntryPrice: 100}}
	copy := copyRound4Case(c)
	copy.Reasons[0] = "new"
	copy.Trade.EntryPrice = 200
	if c.Reasons[0] != "unchanged" || c.Trade.EntryPrice != 100 {
		t.Fatal("reference was mutated")
	}
}
