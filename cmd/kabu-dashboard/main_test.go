package main

import (
	"bytes"
	"context"
	"testing"
)

func TestArguments(t *testing.T) {
	for _, args := range [][]string{{"-port", "0"}, {"-port", "180000"}, {"unexpected"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("expected invalid argument")
		}
	}
	if err := run(context.Background(), []string{"-h"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
