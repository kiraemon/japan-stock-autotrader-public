package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidArgumentsAndMissingData(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"-environment", "wrong"}, {"unexpected"}, {"-environment", "production"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("expected error")
		}
	}
	if err := run(context.Background(), []string{"-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryModeReportsInvalidArchiveWithoutRawData(t *testing.T) {
	t.Chdir(t.TempDir())
	if os.MkdirAll("data/history/production", 0700) != nil {
		t.Fatal("mkdir")
	}
	if os.WriteFile("data/history/production/history-synthetic.json", []byte(`{"schema_version":99,"secret":"synthetic-do-not-log"}`), 0600) != nil {
		t.Fatal("write")
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"-history"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "valid=false") || strings.Contains(output.String(), "synthetic-do-not-log") {
		t.Fatal("history report contract failed")
	}
	files, _ := filepath.Glob("data/quality/history-check-*.json")
	if len(files) != 1 {
		t.Fatal("report not saved")
	}
}
