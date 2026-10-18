package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCaptureHelpAndInvalidRangeDoNotResolveSecrets(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-h"}, &out); err != nil || !strings.Contains(out.String(), "from") {
		t.Fatal("help failed", err)
	}
	for _, args := range [][]string{nil, {"-from", "2026-10-16", "-to", "2026-10-15"}, {"-from", "2026-10-15", "-to", "2026-10-16", "-codes", "0000,0000"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "1Password") {
			t.Fatal("bad arguments reached credentials")
		}
	}
}
