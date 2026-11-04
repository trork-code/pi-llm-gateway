package wizard

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// loadConfigOf はテスト内でconfigを読むヘルパ。
func loadConfigOf(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// loadSecretsOf はidentity由来のsecrets復号。
func loadSecretsOf(t *testing.T, paths setup.Paths) *secrets.Secrets {
	t.Helper()
	sec, err := secrets.Load(secrets.LoadOptions{
		EncryptedPath: paths.Secrets,
		Identity:      bytes.NewReader(mustRead(t, paths.Identity)),
	})
	if err != nil {
		t.Fatalf("secrets復号: %v", err)
	}
	return sec
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// menuEnv はメニュー系テスト用の最小runtime(実環境は触らない)。
type menuEnv struct {
	dir    string
	paths  setup.Paths
	piPath string
}

func newMenuEnv(t *testing.T) *menuEnv {
	t.Helper()
	dir := t.TempDir()
	paths := setup.Layout(dir)
	for _, d := range []string{filepath.Dir(paths.Identity), filepath.Dir(paths.Config)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Identity, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte(
		"default_model: pi-e2e\nmodels:\n  pi-e2e:\n    provider: ollamacloud\n    model: gpt-oss:120b\nproviders:\n  ollamacloud:\n    base_url: https://ollama.com/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sec := &secrets.Secrets{Version: 1, GatewayKeys: []string{"gk-menu"},
		APIKeys: map[string]string{"ollamacloud": "sk-stored-key"}}
	if err := secrets.Save(paths.Secrets, sec, []string{id.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	if err := setup.SaveState(paths, &setup.State{Version: 1, Port: 18450,
		Bind: "127.0.0.1", Startup: "manual", IdentityMode: "file",
		Providers: []string{"ollamacloud"}, Aliases: []string{"pi-e2e"},
		DefaultModel: "pi-e2e", PiRegistered: true}); err != nil {
		t.Fatal(err)
	}
	e := &menuEnv{dir: dir, paths: paths, piPath: filepath.Join(dir, "pi-models.json")}
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	t.Setenv("GATEWAY_PI_MODELS", e.piPath)
	t.Setenv("PORT", "")
	return e
}

// TestMenuAddModel_ReusesStoredKey は既存プロバイダーへのモデル追加で
// APIキー質問をスキップし、保存済みキーを壊さないこと(2026-11要望)。
func TestMenuAddModel_ReusesStoredKey(t *testing.T) {
	e := newMenuEnv(t)
	in := bufio.NewReader(strings.NewReader("1\ngpt-oss:menutest\nn\n"))
	var out strings.Builder
	if err := menuAddModel(e.paths, in, &out); err != nil {
		t.Fatalf("menuAddModel: %v", err)
	}
	s := out.String()
	if strings.Contains(s, "Provide the API key") {
		t.Errorf("既存プロバイダーでキー質問が出ている:\n%s", s)
	}
	if !strings.Contains(s, "key stored (fp sk-s") {
		t.Errorf("fp表示が無い:\n%s", s)
	}
	cfg := loadConfigOf(t, e.paths.Config)
	if cfg.Models["gpt-oss:menutest"].Provider != "ollamacloud" {
		t.Errorf("新モデル登録が無い/不正: %v", cfg.Models)
	}
	sec := loadSecretsOf(t, e.paths)
	if sec.APIKeys["ollamacloud"] != "sk-stored-key" {
		t.Errorf("保存済みキーが壊された: %q", sec.APIKeys["ollamacloud"])
	}
}

// TestMenuAddModel_NewProviderAsksKey はキー未保存のプロバイダー追加時のみキー質問。
func TestMenuAddModel_NewProviderAsksKey(t *testing.T) {
	e := newMenuEnv(t)
	in := bufio.NewReader(strings.NewReader("4\nsk-brand-new\ngroq:llama-3.1-8b-instant\nn\n"))
	var out strings.Builder
	if err := menuAddModel(e.paths, in, &out); err != nil {
		t.Fatalf("menuAddModel: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "Provide the API key for groq") {
		t.Errorf("新規キー質問が無い:\n%s", s)
	}
	if !strings.Contains(s, "added: groq:llama-3.1-8b-instant → groq") {
		t.Errorf("added表示が無い:\n%s", s)
	}
	sec := loadSecretsOf(t, e.paths)
	if sec.APIKeys["groq"] != "sk-brand-new" {
		t.Errorf("groqキーが保存されていない: %v", sec.APIKeys)
	}
	if sec.APIKeys["ollamacloud"] != "sk-stored-key" {
		t.Errorf("既存キーが壊された: %v", sec.APIKeys)
	}
}

// TestMenuManageModels はメニュー3(既定変更/削除)の基本動作。
func TestMenuManageModels(t *testing.T) {
	e := newMenuEnv(t)
	if err := ops.UpsertModel(e.paths.Config, "ollamacloud", "second-model", false); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReader(strings.NewReader("2\n\n")) // 既定→second-model、削除はskip
	var out strings.Builder
	if err := menuManageModels(e.paths, in, &out); err != nil {
		t.Fatalf("menuManageModels: %v", err)
	}
	cfg := loadConfigOf(t, e.paths.Config)
	if cfg.DefaultModel != "second-model" {
		t.Fatalf("既定が変わっていない: %q", cfg.DefaultModel)
	}
}
