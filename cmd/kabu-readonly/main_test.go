package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-autotrader/internal/instanceguard"
	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
)

func TestInvalidArgumentsFailBeforeAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-symbols", "7203@1"},
		{"-symbols", "7203@9", "-password-stdin"},
		{"-symbols", "7203@1", "-environment", "other", "-password-stdin"},
		{"-symbols", "7203@1", "-password-stdin", "unexpected"},
		{"-symbols", "7203@1", "-count", "0"},
		{"-symbols", "7203@1", "-count", "10001"},
		{"-symbols", "7203@1", "-interval", "0s"},
		{"-symbols", "7203@1", "-interval", "2h"},
		{"-symbols", "7203@1", "-onepassword", "-password-stdin"},
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

type unreadableInput struct{ t *testing.T }

func (r unreadableInput) Read([]byte) (int, error) {
	r.t.Fatal("password was read before acquiring the session")
	return 0, nil
}

func TestSecondInstanceRejectedBeforePasswordRead(t *testing.T) {
	var output bytes.Buffer
	err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-password-stdin"}, unreadableInput{t}, &output,
		func(kabu.Environment) (session, error) { return nil, instanceguard.ErrHeld })
	if !errors.Is(err, instanceguard.ErrHeld) {
		t.Fatal(err)
	}
}

func TestSecondInstanceRejectedBeforeOnePasswordConfiguration(t *testing.T) {
	var output bytes.Buffer
	err := runWithSession(context.Background(), []string{"-symbols", "7203@1", "-onepassword"}, unreadableInput{t}, &output,
		func(kabu.Environment) (session, error) { return nil, instanceguard.ErrHeld })
	if !errors.Is(err, instanceguard.ErrHeld) {
		t.Fatal("credential resolution preceded the startup gate")
	}
}
