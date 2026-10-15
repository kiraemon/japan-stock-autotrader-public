package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPowerShellConfigurationAndDPAPI(t *testing.T) {
	// Copy setup into an isolated project; it never writes real configuration.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "configure-onepassword.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "configure-onepassword.ps1")
	if err := os.WriteFile(script, source, 0600); err != nil {
		t.Fatal(err)
	}
	command := `function Read-Host { ConvertTo-SecureString 'synthetic-service-token' -AsPlainText -Force }; & $env:JSA_CONFIG_TEST_SCRIPT -Environment test -SecretReference 'op://test-vault/item/password'`
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Env = append(os.Environ(), "JSA_CONFIG_TEST_SCRIPT="+script, "PSModulePath="+filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "Modules"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell configuration failed: %s", output)
	}
	if bytes.Contains(output, []byte("synthetic-service-token")) {
		t.Fatal("token leaked to output")
	}
	raw, err := os.ReadFile(filepath.Join(root, "secrets", "onepassword.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("synthetic-service-token")) {
		t.Fatal("token stored in plaintext")
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	plain, err := unprotect(cfg.ProtectedToken)
	defer clear(plain)
	if err != nil || string(plain) != "synthetic-service-token" {
		t.Fatal("PowerShell/Go DPAPI interoperability failed")
	}
	if _, err := unprotect([]byte("not a DPAPI blob")); err == nil {
		t.Fatal("invalid blob accepted")
	}
	if _, err := resolve(context.Background(), root, "production", unprotect, readSecret); err == nil {
		t.Fatal("unconfigured production accepted")
	}
	// Adding a production reference must reuse ciphertext without prompting.
	cmd = exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", `& $env:JSA_CONFIG_TEST_SCRIPT -Environment production -SecretReference 'op://test-vault/production/password' -ReuseToken`)
	cmd.Env = append(os.Environ(), "JSA_CONFIG_TEST_SCRIPT="+script, "PSModulePath="+filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "Modules"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reuse failed: %s", output)
	}
	updated, _ := os.ReadFile(filepath.Join(root, "secrets", "onepassword.json"))
	var other config
	if err := json.Unmarshal(updated, &other); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cfg.ProtectedToken, other.ProtectedToken) || other.References["test"] == "" || other.References["production"] == "" {
		t.Fatal("reuse changed token or lost environment")
	}
}
