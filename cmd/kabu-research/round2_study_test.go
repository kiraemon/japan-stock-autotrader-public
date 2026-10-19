package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/prepareorb"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/researchbook"
	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

func TestRound2LegacyCannotUseP0AvailabilityOrRVOL(t *testing.T) {
	no, yes := false, true
	distance := int64(100)
	old := prepareorb.Report{OpeningPolicy: prepareorb.JQuantsPrefixPolicy, Symbol: "0000@1"}
	old.Target.Date = "2025-03-03"
	c := s01InputCase{Date: old.Target.Date, InputsAvailable: true, RvolPassed: true, Opening: prepareorb.Report{OpeningPolicy: prepareorb.PastVolumePolicy, Symbol: old.Symbol}, LegacyOpening: &old, LegacyAvailable: &no, LegacyRvolPassed: &no, ATR: s01.ATRReport{Available: true, StopDistance: &distance}}
	before, _ := json.Marshal(c)
	r, err := round2LegacyInput(c)
	if err != nil || r.InputsAvailable || r.RvolPassed || r.Opening.CalculationAvailable || len(r.Problems) != 1 || r.Problems[0] != "opening_rvol_unavailable" {
		t.Fatal("new input promoted legacy rule", err)
	}
	after, _ := json.Marshal(c)
	if !bytes.Equal(before, after) {
		t.Fatal("input changed")
	}
	c.LegacyAvailable = &yes
	if _, err := round2LegacyInput(c); err == nil {
		t.Fatal("legacy readiness mismatch admitted")
	}
	c.LegacyAvailable = &no
	c.LegacyOpening = nil
	if _, err := round2LegacyInput(c); err == nil {
		t.Fatal("missing legacy provenance admitted")
	}
	row, err := round2PriceCase(context.Background(), marketdata.Dataset{Symbol: old.Symbol}, s01InputCase{Date: old.Target.Date, Problems: []string{"opening_rvol_unavailable"}}, marketdata.DailyBar{}, "R2-P0")
	if err != nil || row.Status != "unevaluable" || row.Trade != nil || !reflect.DeepEqual(row.Reasons, []string{"opening_rvol_unavailable"}) {
		t.Fatal("missing input not retained", err)
	}
}

func TestRound2CohortsKeepLegacyNewAllAndPairsDistinct(t *testing.T) {
	rows := make([]researchbook.Case, 8)
	for i := range rows {
		rows[i] = researchbook.Case{Date: "2025-03-03", Symbol: "0000@1", Rank: 1, Status: "no_trade"}
	}
	rows[1].Status = "unevaluable"
	c, err := round2CaseCohorts(rows)
	if err != nil || c.Legacy || c.All || !c.New || !c.Pairs["R2-P0"] {
		t.Fatal("missing old A should not promote all-eight or suppress new/pair cohorts")
	}
	rows[0].Status = "unevaluable"
	c, err = round2CaseCohorts(rows)
	if err != nil || !c.New || c.Pairs["R2-P0"] || c.Pairs["R2-P1"] || c.Pairs["R2-P2"] {
		t.Fatal("missing old S01 lost in paired cohort")
	}
	rows[5].Status = "unevaluable"
	c, _ = round2CaseCohorts(rows)
	if c.New {
		t.Fatal("new-family missing case dropped")
	}
	rows[0].Status = "no_trade"
	rows[5].Status = "no_trade"
	c, _ = round2CaseCohorts(rows)
	selected := round2SelectCohort(rows[:1], map[string]round2Cohorts{"2025-03-03/0000@1": c}, "pair_R2-P0")
	if !selected[0].Common || rows[0].Common {
		t.Fatal("paired selection or ownership changed")
	}
	if len(round2BookCohorts(s01.Version)) != 5 || len(round2BookCohorts("R2-P0")) != 3 || len(round2BookCohorts("R001-A-long")) != 2 {
		t.Fatal("cohort book inventory changed")
	}
	if _, err := round2CaseCohorts(rows[:7]); err == nil {
		t.Fatal("incomplete matrix allowed")
	}
	rows[7].Date = "2025-03-04"
	if _, err := round2CaseCohorts(rows); err == nil {
		t.Fatal("cross-date comparison allowed")
	}
}

func TestRound2GateAddsDrawdownAndOwnCoverageWithoutRelaxingLegacyRejection(t *testing.T) {
	r := researchbook.Report{Cases: 100, Unevaluable: 20, Decision: "paper_research_candidate", Drawdown: "40000", GateReasons: []string{}}
	g, err := round2Gate(r)
	if err != nil || g.Decision != "paper_research_candidate" || g.ScopeSHA != s01.Round2ScopeSHA256 {
		t.Fatal("inclusive boundary changed")
	}
	r.Drawdown = "4000001/100"
	g, _ = round2Gate(r)
	if g.Decision != "hold" || len(g.Reasons) != 1 || len(r.GateReasons) != 0 {
		t.Fatal("drawdown failure or ownership lost")
	}
	r.Unevaluable = 21
	r.Decision = "reject_this_version"
	g, _ = round2Gate(r)
	if g.Decision != "reject_this_version" || len(g.Reasons) != 2 {
		t.Fatal("negative result promoted or own coverage ignored")
	}
	r.Drawdown = "private-marker"
	if _, err := round2Gate(r); err == nil {
		t.Fatal("invalid drawdown allowed")
	}
}

func TestRound2HoldoutUsesSeparatePersistentVersionAndRejectsUnfrozenBuild(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := reserveStudyOpening("old", "old", "old"); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join("data", "research", "s01-holdout-opening-20261019.json")
	before, _ := os.ReadFile(oldPath)
	if err := reserveStudyOpeningMode("new", "new", "new", true); err != nil {
		t.Fatal(err)
	}
	if err := reserveStudyOpeningMode("new", "new", "new", true); err != nil {
		t.Fatal("identical replay refused")
	}
	if err := reserveStudyOpeningMode("changed", "new", "new", true); err == nil {
		t.Fatal("retuned holdout allowed")
	}
	after, _ := os.ReadFile(oldPath)
	if !bytes.Equal(before, after) {
		t.Fatal("old research opening overwritten")
	}
	if os.WriteFile("freeze.json", []byte(`{"version":1}`), 0600) != nil {
		t.Fatal("test fixture write")
	}
	if _, err := checkRound2Freeze("freeze.json", []byte("manifest")); err == nil {
		t.Fatal("unidentified build allowed")
	}
	for _, args := range [][]string{{"study-s01-round2", "-phase", "holdout"}, {"study-s01-round2", "-phase", "development", "-freeze", "freeze.json"}, {"study-s01-round2", "-phase", "all"}} {
		if run(context.Background(), args, &bytes.Buffer{}) == nil {
			t.Fatal("unsafe round2 phase allowed")
		}
	}
}
