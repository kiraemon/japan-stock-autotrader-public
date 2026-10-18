package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("JSA_CREDENTIAL_TEST_CHILD"); mode != "" {
		if len(os.Args) != 5 || os.Args[1] != "read" || os.Args[2] != "op://test-vault/item/password" || os.Args[3] != "--no-newline" || os.Args[4] != "--cache=false" {
			os.Exit(3)
		}
		if os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") != "synthetic-service-token" || os.Getenv("OP_BIOMETRIC_UNLOCK_ENABLED") != "false" || os.Getenv("OP_CONNECT_TOKEN") != "" {
			os.Exit(4)
		}
		switch mode {
		case "ok":
			fmt.Print("synthetic-api-password")
		case "fail":
			fmt.Fprint(os.Stderr, "synthetic-service-token synthetic-api-password")
			os.Exit(2)
		case "multiline":
			fmt.Print("synthetic-api-password\nextra")
		case "oversize":
			fmt.Print(strings.Repeat("x", 2000))
		case "sleep":
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestReadSecretNeverEchoesFailures(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ok", "fail", "multiline", "oversize", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("JSA_CREDENTIAL_TEST_CHILD", mode)
			t.Setenv("OP_CONNECT_TOKEN", "must-not-be-inherited")
			ctx := context.Background()
			if mode == "sleep" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			value, err := readSecret(ctx, executable, "op://test-vault/item/password", []byte("synthetic-service-token"))
			if mode == "ok" {
				if err != nil || value != "synthetic-api-password" {
					t.Fatal("secret resolution failed")
				}
			} else {
				if err == nil || value != "" {
					t.Fatal("invalid secret output was accepted")
				}
				if strings.Contains(err.Error(), "synthetic-") {
					t.Fatal("secret leaked in error")
				}
			}
		})
	}
}

func TestChildEnvironmentHasNoOtherOnePasswordAuthentication(t *testing.T) {
	result := childEnvironment([]string{"PATH=test", "OP_SESSION_x=old", "op_connect_token=old", "OP_ACCOUNT=old", "OP_SERVICE_ACCOUNT_TOKEN=old"}, []byte("synthetic-service-token"))
	if len(result) != 3 || result[0] != "PATH=test" {
		t.Fatal("other authentication inherited")
	}
}

func TestJQuantsReferenceDoesNotChangeBrokerEnvironment(t *testing.T) {
	if _, err := OnePassword(context.Background(), t.TempDir(), "jquants"); err == nil {
		t.Fatal("broker resolved a data-service key")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(config{Version: 1, References: map[string]string{"jquants": "op://test-vault/item/password"}, ProtectedToken: []byte("synthetic-encrypted")})
	if err := os.WriteFile(filepath.Join(root, "secrets", "onepassword.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	value, err := resolve(context.Background(), root, "jquants", func([]byte) ([]byte, error) { return []byte("synthetic-service-token"), nil }, func(_ context.Context, _ string, reference string, _ []byte) (string, error) {
		if reference != "op://test-vault/item/password" {
			t.Fatal("wrong data-service reference")
		}
		return "synthetic-jquants-key", nil
	})
	if err != nil || value != "synthetic-jquants-key" {
		t.Fatal("data-service resolution failed", err)
	}
}

func TestResolveIsEnvironmentSpecificAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(config{Version: 1, References: map[string]string{"test": "op://test-vault/item/password"}, ProtectedToken: []byte("synthetic-ciphertext")})
	if err := os.WriteFile(filepath.Join(root, "secrets", "onepassword.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	decryptCalls := 0
	decrypt := func([]byte) ([]byte, error) { decryptCalls++; return []byte("synthetic-service-token"), nil }
	reads := 0
	read := func(_ context.Context, executable, reference string, token []byte) (string, error) {
		reads++
		if executable != filepath.Join(root, ".tools", "onepassword", "op.exe") || reference != "op://test-vault/item/password" {
			t.Fatal("wrong executable/reference")
		}
		return "synthetic-api-password", nil
	}
	if _, err := resolve(context.Background(), root, "production", decrypt, read); err == nil || decryptCalls != 0 || reads != 0 {
		t.Fatal("unconfigured production used test credentials")
	}
	if _, err := resolve(context.Background(), root, "test", func([]byte) ([]byte, error) { return nil, errors.New("synthetic-service-token") }, read); err == nil || strings.Contains(err.Error(), "synthetic-") || reads != 0 {
		t.Fatal("decryption failure leaked or fell back")
	}
	value, err := resolve(context.Background(), root, "test", decrypt, read)
	if err != nil || value != "synthetic-api-password" || decryptCalls != 1 || reads != 1 {
		t.Fatal("configured reference failed")
	}
}

func TestReferenceValidation(t *testing.T) {
	for _, reference := range []string{"", "https://example.com", "op://vault/item", "op://vault//password", "op://vault/item/password?attribute=otp", "op://vault/item/password\n"} {
		if ValidReference(reference) {
			t.Fatal("invalid secret reference accepted")
		}
	}
	if !ValidReference("op://test vault/test item/section/password") {
		t.Fatal("valid reference rejected")
	}
}
