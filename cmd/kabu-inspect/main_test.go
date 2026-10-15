package main

import (
	"bytes"
	"context"
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
