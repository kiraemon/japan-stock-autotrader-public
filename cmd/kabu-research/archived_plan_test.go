package main

import (
	"errors"
	"os"
	"testing"
)

func readArchivedPlan(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("original research plan is kept outside the public repository")
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
