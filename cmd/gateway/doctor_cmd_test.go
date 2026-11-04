package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// doctorCmdEnv は doctorCmd のexit code契約を検証するための最小環境。
// GATEWAY_RUNTIME_DIR / GATEWAY_PI_MODELS フックで実環境は触らない。
func buildDoctorCmdEnv(t *testing.T) (dir, piFile string, gwKey string) {
	t.Helper()
	dir = t.TempDir()
	paths := setup.Layout(dir)

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Identity), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Identity, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	gwKey = "gk-doctorcmd-test-key"
	sec := &secrets.Secrets{Version: 1, GatewayKeys: []string{gwKey},
		APIKeys: map[string]string{"ollamacloud": "sk-x"}}
	if err := secrets.Save(paths.Secrets, sec, []string{id.Recipient().String()}); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte(
		"default_model: a1\nmodels:\n  a1:\n    provider: ollamacloud\n    model: m\nproviders:\n  ollamacloud:\n    base_url: https://ollama.com/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 空きポート(固定ポート衝突回避)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	st := &setup.State{Version: 1, Port: port, Bind: "127.0.0.1", Startup: "manual",
		IdentityMode: "file", Providers: []string{"ollamacloud"},
		Aliases: []string{"a1"}, DefaultModel: "a1", PiRegistered: true}
	if err := setup.SaveState(paths, st); err != nil {
		t.Fatal(err)
	}

	// bin/ランチャ(実行は失敗するダミー → 9はwarnに落ちる=exit codeに影響しない)
	if err := os.MkdirAll(paths.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Bin, []byte("not-an-exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Launcher, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pi := map[string]any{"providers": map[string]any{"gateway": map[string]any{
		"api": "openai-completions", "apiKey": gwKey,
		"baseUrl": "http://127.0.0.1:" + strconv.Itoa(port) + "/v1",
		"models":  []map[string]any{{"id": "a1", "_launch": true, "reasoning": false}},
	}}}
	pidata, _ := json.Marshal(pi)
	piFile = filepath.Join(dir, "pi-models.json")
	if err := os.WriteFile(piFile, pidata, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	t.Setenv("GATEWAY_PI_MODELS", piFile)
	return dir, piFile, gwKey
}

// TestDoctorCmdExitCodes: 全環境ok相当 → 0 / piキー不一致 → 1。
func TestDoctorCmdExitCodes(t *testing.T) {
	buildDoctorCmdEnv(t)
	code, err := doctorCmd(nil, &strings.Builder{})
	if err != nil {
		t.Fatalf("doctorCmd: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0 (warnのみの環境)", code)
	}

	// piキー不一致 → item4 fail → exit 1
	piData, _ := json.Marshal(map[string]any{"providers": map[string]any{"gateway": map[string]any{
		"api": "openai-completions", "apiKey": "gk-mismatched",
	}}})
	piPath := os.Getenv("GATEWAY_PI_MODELS")
	if err := os.WriteFile(piPath, piData, 0o600); err != nil {
		t.Fatal(err)
	}
	code, err = doctorCmd(nil, &strings.Builder{})
	if err != nil {
		t.Fatalf("doctorCmd(2): %v", err)
	}
	if code != 1 {
		t.Errorf("code = %d, want 1 (pi key mismatch)",
			code)
	}
}

// TestModelsCmd はエイリアス表が表示されること。
func TestModelsCmd(t *testing.T) {
	buildDoctorCmdEnv(t)
	var out strings.Builder
	if err := modelsCmd(nil, &out); err != nil {
		t.Fatalf("modelsCmd: %v", err)
	}
	s := out.String()
	for _, want := range []string{"ALIAS", "a1", "ollamacloud", "*"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q not found in:\n%s", want, s)
		}
	}
}
