package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestInvalidArgumentsFailBeforeAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-symbols", "7203@1"},
		{"-symbols", "7203@9", "-password-stdin"},
		{"-symbols", "7203@1", "-environment", "other", "-password-stdin"},
		{"-symbols", "7203@1", "-password-stdin", "unexpected"},
	} {
		var output bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader("test-only-password\n"), &output); err == nil {
			t.Fatalf("expected validation failure for %v", args)
		}
		if strings.Contains(output.String(), "test-only-password") {
			t.Fatal("credential leaked")
		}
	}
}
