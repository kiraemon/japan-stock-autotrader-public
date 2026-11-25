package s01

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

func TestRound2ScopeMatchesPredeclaredFileAndRejectsCombinationSearch(t *testing.T) {
	b, err := os.ReadFile("../../../examples/research/s01-round2-scope.plan.json")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("original research plan is kept outside the public repository")
	}
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != Round2ScopeSHA256 {
		t.Fatal("fixed round2 scope changed")
	}
	if _, err := Round2Rule("R2-P1+P2"); err == nil {
		t.Fatal("undeclared combined candidate admitted")
	}
	p, _ := Round2Rule("R2-P2")
	p.MinimumRVOL = "0"
	again, _ := Round2Rule("R2-P2")
	if again.MinimumRVOL != "3/2" {
		t.Fatal("caller mutated the fixed policy")
	}
}

func TestRound2DistanceRoundsATRBeforeAnyTradeOnce(t *testing.T) {
	// ATR=101/3: ceil(ATR/10)=4 and ceil(ATR/5)=7, not 2*4.
	for _, c := range []struct {
		id       string
		expected int64
	}{{"R2-P0", 4}, {"R2-P1", 7}, {"R2-P2", 4}} {
		distance, err := Round2StopDistance(c.id, "101/3")
		if err != nil || distance != c.expected {
			t.Fatal("fraction or rounding changed", c.id, distance, err)
		}
	}
	for _, value := range []string{"0", "-1", "1/0", "1.5", "1000000000001"} {
		if _, err := Round2StopDistance("R2-P1", value); err == nil {
			t.Fatal("invalid ATR admitted", value)
		}
	}
}
