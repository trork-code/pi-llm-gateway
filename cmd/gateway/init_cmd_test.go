package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// TestInitYes_ReuseStoredProviderKey は非対話init(--yes)で --api-key を省略した場合に
// 保存済みのproviderキーが流用されることを検証する(設計: キーはプロバイダー追加時のみ)。
// 入口(initCmd)の必須チェックとRunSetup内の流用判定の配線を両方通す一貫性ガード。
func TestInitYes_ReuseStoredProviderKey(t *testing.T) {
	base := t.TempDir()
	runtimeDir := filepath.Join(base, "rt")
	t.Setenv("GATEWAY_RUNTIME_DIR", runtimeDir)
	t.Setenv("GATEWAY_PI_MODELS", filepath.Join(base, "pi-models.json")) // --no-piだが誤触の隔離として保険
	t.Setenv("INIT_TEST_PROVIDER_KEY", "stored-provider-key-123")

	common := []string{"--provider", "ollamacloud", "--model", "gpt-oss:120b",
		"--yes", "--no-pi", "--no-start", "--quiet"}

	// 1回目: キーをsecretsに保存
	first := append(append([]string{"init"}, common...), "--api-key", "env:INIT_TEST_PROVIDER_KEY")
	if err := initCmd(first[1:], io.Discard); err != nil {
		t.Fatalf("初回init(--api-keyあり)が失敗: %v", err)
	}

	// 2回目: --api-key無し → 保存済みキーの流用で成功すべき
	if err := initCmd(common, io.Discard); err != nil {
		t.Fatalf("--api-key無しの非対話initが失敗(流用経路が開通していない): %v", err)
	}

	// 流用後もキー値が変質していないこと(静かな上書き事故の廃止を担保)
	paths := setup.Layout(runtimeDir)
	idData, err := os.ReadFile(paths.Identity)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.Load(secrets.LoadOptions{EncryptedPath: paths.Secrets,
		Identity: strings.NewReader(strings.TrimSpace(string(idData)))})
	if err != nil {
		t.Fatal(err)
	}
	if got := sec.APIKeys["ollamacloud"]; got != "stored-provider-key-123" {
		t.Fatalf("流用したproviderキーが変質している: %q", got)
	}
	if _, err := os.Stat(paths.SetupJSON); err != nil {
		t.Fatalf("setup.jsonが生成されていない: %v", err)
	}
}

// TestInitYes_APIKeyRequiredOnCleanRuntime はクリーン環境(キー未保存)での
// --api-key省略がfail-closedになることを検証する(流用は保存済みキーがあるときだけ許可)。
func TestInitYes_APIKeyRequiredOnCleanRuntime(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "rt-clean")
	t.Setenv("GATEWAY_RUNTIME_DIR", runtimeDir)

	err := initCmd([]string{"--provider", "ollamacloud", "--model", "gpt-oss:120b",
		"--yes", "--no-pi", "--no-start", "--quiet"}, io.Discard)
	if err == nil {
		t.Fatal("クリーン環境+--api-key無しはfail-closedであるべき")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Fatalf("期待のエラー文言でない: %v", err)
	}
}
