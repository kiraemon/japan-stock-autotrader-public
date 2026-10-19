package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHoldoutOpeningAllowsOnlyIdenticalVersionReplay(t *testing.T) {
	t.Chdir(t.TempDir())
	a, b, rev := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 40)
	if err := reserveStudyOpening(a, b, rev); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("data", "research", "s01-holdout-opening-20261019.json")
	first, _ := os.ReadFile(path)
	if err := reserveStudyOpening(a, b, rev); err != nil {
		t.Fatal("identical replay refused", err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(first, again) {
		t.Fatal("opening record replaced")
	}
	for _, v := range [][3]string{{b, b, rev}, {a, a, rev}, {a, b, strings.Repeat("d", 40)}} {
		if err := reserveStudyOpening(v[0], v[1], v[2]); err == nil {
			t.Fatal("holdout reopened under changed version")
		}
	}
	if os.WriteFile(path, []byte("private-marker"), 0600) != nil {
		t.Fatal("fixture write")
	}
	if err := reserveStudyOpening(a, b, rev); err == nil || strings.Contains(err.Error(), "private-marker") {
		t.Fatal("invalid opening accepted or leaked")
	}
}
func TestHoldoutRejectsUnfrozenOrUnidentifiedBuildBeforeAnyCalculation(t *testing.T) {
	t.Chdir(t.TempDir())
	if os.WriteFile("freeze.json", []byte(`{"version":1}`), 0600) != nil {
		t.Fatal("fixture write")
	}
	if _, err := checkStudyFreeze("freeze.json", []byte("manifest")); err == nil {
		t.Fatal("unfrozen build admitted")
	}
	for _, args := range [][]string{{"study-s01", "-phase", "holdout"}, {"study-s01", "-phase", "development", "-freeze", "freeze.json"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid phase/freeze combination admitted")
		}
	}
}
