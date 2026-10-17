package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/features/assesscapital"
	"github.com/kiraemon/japan-stock-autotrader/internal/features/backtest"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

func capitalFixture(t *testing.T) (backtest.Report, assesscapital.Assumptions) {
	t.Helper()
	r, err := backtest.Run(context.Background(), backtest.Synthetic(r001.Long), r001.Long)
	if err != nil {
		t.Fatal(err)
	}
	return r, assesscapital.Assumptions{CapitalJPY: 400000, Quantity: 100, LotSize: 100, Mode: "cash", InitialMarginBPS: 10000, FrictionBPS: 10}
}

func TestCapitalCLIRecordsAssumptionsAndPreservesSource(t *testing.T) {
	t.Chdir(t.TempDir())
	source, a := capitalFixture(t)
	raw, _ := json.Marshal(source)
	assumptionRaw, _ := json.Marshal(a)
	if os.WriteFile("source.json", raw, 0600) != nil || os.WriteFile("assumptions.json", assumptionRaw, 0600) != nil {
		t.Fatal("fixture")
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"capital-check", "-input", "source.json", "-assumptions", "assumptions.json"}, &out); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("data/research/capital-assessment-*.json")
	manifests, _ := filepath.Glob("data/research/research-run-*.json")
	if len(files) != 1 || len(manifests) != 1 {
		t.Fatal("missing outputs")
	}
	resultRaw, _ := os.ReadFile(files[0])
	var r capitalReport
	if json.Unmarshal(resultRaw, &r) != nil || r.InputSHA256 != hash(raw) || r.AssumptionsSHA256 != hash(assumptionRaw) || len(r.Days) != 1 || r.Days[0].Assessment == nil || r.TradingEligible || r.StrategyAdopted || r.PortfolioMode {
		t.Fatal("provenance or scope")
	}
	after, _ := os.ReadFile("source.json")
	if !bytes.Equal(after, raw) {
		t.Fatal("source changed")
	}
	if !strings.Contains(out.String(), "trading_eligible=false") {
		t.Fatal("missing boundary")
	}
	t.Log("synthetic=true / source_and_assumptions_hashes=recorded / independent_trades=true / trading_eligible=false")
}

func TestCapitalAssumptionsRequireExplicitZeroCosts(t *testing.T) {
	_, a := capitalFixture(t)
	raw, _ := json.Marshal(a)
	for _, changed := range [][]byte{bytes.Replace(raw, []byte(`"round_trip_credit_cost_jpy":0`), []byte(`"round_trip_credit_cost_jpy":null`), 1), bytes.Replace(raw, []byte(`,"round_trip_credit_cost_jpy":0`), nil, 1), append([]byte(`{"unknown":1,`), raw[1:]...)} {
		if _, err := decodeCapitalAssumptions(changed); err == nil {
			t.Fatal("implicit/unknown costs accepted")
		}
	}
}

func TestCapitalCheckDoesNotConvertUnknownDaysToZeroTrades(t *testing.T) {
	source, a := capitalFixture(t)
	source.Days[0].Status = "unevaluable"
	source.Days[0].Reasons = []string{"missing_data"}
	source.Days[0].Trade = nil
	r, err := assessResearchCapital(context.Background(), source, a)
	if err != nil || len(r.Days) != 1 || r.Days[0].Assessment != nil || r.Days[0].Source.Status != "unevaluable" || r.Days[0].Source.Reasons[0] != "missing_data" {
		t.Fatal("unknown day erased")
	}
	source.Scope = "price_pattern_only"
	if _, err := assessResearchCapital(context.Background(), source, a); err == nil {
		t.Fatal("unverified source scope accepted")
	}
}
