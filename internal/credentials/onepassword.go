// Package credentials resolves a configured API password without printing secrets.
package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	Version        int               `json:"version"`
	References     map[string]string `json:"references"`
	ProtectedToken []byte            `json:"protected_token"`
}

// OnePassword is called only after the caller acquires the broker session gate.
// root is the application project directory, never an arbitrary command path.
func OnePassword(ctx context.Context, root, environment string) (string, error) {
	if environment != "test" && environment != "production" {
		return "", errors.New("接続環境が不正です")
	}
	return onePassword(ctx, root, environment)
}

// JQuants resolves only the separately configured market-data API key.
// The caller acquires the J-Quants connection gate before resolving it.
func JQuants(ctx context.Context, root string) (string, error) {
	return onePassword(ctx, root, "jquants")
}

func onePassword(ctx context.Context, root, environment string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("プロジェクトのパスを解決できません")
	}
	return resolve(ctx, absolute, environment, unprotect, readSecret)
}

func resolve(ctx context.Context, root, environment string,
	decrypt func([]byte) ([]byte, error), read func(context.Context, string, string, []byte) (string, error)) (string, error) {
	if environment != "test" && environment != "production" && environment != "jquants" {
		return "", errors.New("接続環境が不正です")
	}
	f, err := os.Open(filepath.Join(root, "secrets", "onepassword.json"))
	if err != nil {
		return "", errors.New("1Passwordのローカル設定がありません。configure-onepassword.ps1で登録してください")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return "", errors.New("1Password設定を読み取れません")
	}
	defer clear(raw)
	var cfg config
	if json.Unmarshal(raw, &cfg) != nil || cfg.Version != 1 || len(cfg.ProtectedToken) == 0 {
		return "", errors.New("1Password設定の形式が不正です")
	}
	reference := cfg.References[environment]
	if !ValidReference(reference) {
		return "", errors.New("選んだ環境の1Password秘密参照が未登録または不正です")
	}
	token, err := decrypt(cfg.ProtectedToken)
	if err != nil {
		return "", errors.New("1Passwordトークンを復号できません。登録したWindowsユーザー・PCを確認してください")
	}
	defer clear(token)
	if len(token) < 16 || len(token) > 16384 || bytes.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("保存された1Passwordトークンの形式が不正です")
	}
	return read(ctx, filepath.Join(root, ".tools", "onepassword", "op.exe"), reference, token)
}

func ValidReference(reference string) bool {
	if !strings.HasPrefix(reference, "op://") || len(reference) > 4096 || strings.ContainsAny(reference, "\r\n\x00?#\\") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(reference, "op://"), "/")
	if len(parts) != 3 && len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

func childEnvironment(base []string, token []byte) []string {
	var result []string
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "OP_") {
			result = append(result, entry)
		}
	}
	return append(result, "OP_SERVICE_ACCOUNT_TOKEN="+string(token), "OP_BIOMETRIC_UNLOCK_ENABLED=false")
}

// limitedOutput drains stdout while bounding the amount retained in memory.
type limitedOutput struct {
	value    []byte
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 1024 - len(b.value)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.value = append(b.value, p...)
	return n, nil
}

func readSecret(ctx context.Context, executable, reference string, token []byte) (string, error) {
	if len(token) == 0 {
		return "", errors.New("1Passwordトークンがありません")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "read", reference, "--no-newline", "--cache=false")
	cmd.Env = childEnvironment(os.Environ(), token)
	defer func() {
		for i := range cmd.Env {
			cmd.Env[i] = ""
		}
	}()
	var output limitedOutput
	defer func() { clear(output.value) }()
	cmd.Stdout = &output
	cmd.Stderr = io.Discard // CLI errors may contain sensitive data; never echo them.
	if cmd.Run() != nil {
		return "", errors.New("1Password取得に失敗しました。CLI・トークン・権限・接続を確認してください（詳細は出力しません）")
	}
	if output.overflow || len(output.value) == 0 || bytes.ContainsAny(output.value, "\r\n\x00") {
		return "", errors.New("1Passwordから取得したAPIパスワードの形式が不正です")
	}
	return string(output.value), nil
}
