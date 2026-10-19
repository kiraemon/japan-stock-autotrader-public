package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/researchbook"
)

func TestRound3CohortsKeepUnavailableAndSeparateReference(t *testing.T) {
	rows := []researchbook.Case{{Date: "2024-12-02", Symbol: "0000@1", Rank: 1, Status: "unevaluable"}, {Date: "2024-12-02", Symbol: "0000@1", Rank: 1, Status: "no_trade"}, {Date: "2024-12-02", Symbol: "0000@1", Rank: 1, Status: "trade"}}
	c, e := round3Cohorts(rows)
	if e != nil || !c["new_two"] || c["all_three"] || c["pair_"+round3Models[1]] || len(rows) != 3 {
		t.Fatal("unavailable reference changed new-two cohort")
	}
	rows[1].Status = "unevaluable"
	c, e = round3Cohorts(rows)
	if e != nil || c["new_two"] {
		t.Fatal("unavailable new candidate admitted")
	}
	rows[2].Rank = 2
	if _, e := round3Cohorts(rows); e == nil {
		t.Fatal("mismatched rank admitted")
	}
}

func TestRound3GateUsesOwnCoverageAndRealizedDrawdown(t *testing.T) {
	r := researchbook.Report{Cases: 100, Unevaluable: 20, Drawdown: "40000", Decision: "paper_research_candidate"}
	g, e := round3Gate(r)
	if e != nil || g.Decision != "paper_research_candidate" || len(g.Reasons) != 0 {
		t.Fatal("exact boundaries rejected")
	}
	r.Unevaluable = 21
	r.Drawdown = "40001"
	g, e = round3Gate(r)
	if e != nil || g.Decision != "hold" || len(g.Reasons) != 2 || g.ScopeSHA != round3ScopeSHA {
		t.Fatal("fixed own-coverage/DD gates missing")
	}
	r.Decision = "reject_this_version"
	r.GateReasons = []string{"nonpositive_net_after_allocated_data_fee"}
	g, e = round3Gate(r)
	if e != nil || g.Decision != "reject_this_version" || len(r.GateReasons) != 1 {
		t.Fatal("negative result promoted or input mutated")
	}
}

func TestRound3UnfrozenFinalRejectedBeforeInputRead(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"study-round3", "-input", "missing", "-phase", "holdout"}, {"study-round3", "-input", "missing", "-phase", "validation", "-freeze", "missing"}} {
		var out bytes.Buffer
		e := run(context.Background(), args, &out)
		if e == nil || !strings.Contains(e.Error(), "固定後") || out.Len() != 0 {
			t.Fatal("phase guard ran after input or produced results")
		}
	}
	manifest := []byte("manifest")
	rev := strings.Repeat("a", 40)
	f := studyFreeze{Version: 1, Revision: rev, OriginalRevision: rev, ManifestSHA: hash(manifest), PlanSHA: round3ScopeSHA}
	body, _ := json.Marshal(f)
	if e := os.WriteFile("freeze.json", body, 0600); e != nil {
		t.Fatal(e)
	}
	for _, identity := range [][2]string{{rev, "true"}, {strings.Repeat("b", 40), "false"}, {"", "false"}} {
		if _, e := checkRound3FreezeIdentity("freeze.json", manifest, identity[0], identity[1]); e == nil {
			t.Fatal("changed/unidentified build admitted")
		}
	}
	p := filepath.Join("data", "research", "round3-holdout-opening-20261019.json")
	if e := reserveStudyOpeningAt(p, strings.Repeat("a", 64), strings.Repeat("b", 64), rev); e != nil {
		t.Fatal(e)
	}
	if e := reserveStudyOpeningAt(p, strings.Repeat("a", 64), strings.Repeat("b", 64), rev); e != nil {
		t.Fatal("identical replay rejected")
	}
	if e := reserveStudyOpeningAt(p, strings.Repeat("c", 64), strings.Repeat("b", 64), rev); e == nil {
		t.Fatal("different final version admitted")
	}
}

func TestRound3FixedPlansRejectBeforeArtifactReads(t *testing.T) {
	for _, m := range []round3Manifest{{Version: 1}, {Version: 1, Scope: studyArtifact{SHA: strings.Repeat("0", 64)}, Period: studyArtifact{SHA: round3PeriodSHA}}} {
		raw, _ := json.Marshal(m)
		if _, e := loadRound3Inputs(context.Background(), "missing", raw, "validation"); e == nil {
			t.Fatal("unfixed plans admitted")
		}
	}
}
