package main

// runtime_build_test.go — buildRuntime とwizardカタログの一貫性検証。
//
// 2026-10-28 発見のバグ(別PC配布テストで報告):
//   ウィザードは nvidia / groq / openrouter を選択肢に提示しているが、
//   buildRuntime の switch への配線が漏れており、「未知のprovider」で
//   常に起動失敗していた(adapter自体は2026-09-09に実装済みだった)。
//   本テストは「カタログの全enabled providerがregistryへ接続される」ことを
//   検証して再発を防ぐ。

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/providers"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/wizard"
)

func TestBuildRuntimeRegistersAllCatalogProviders(t *testing.T) {
	dir := t.TempDir()

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("identityの生成: %v", err)
	}
	identFile := filepath.Join(dir, "identity.txt")
	if err := os.WriteFile(identFile, []byte(id.String()), 0o600); err != nil {
		t.Fatalf("identityファイルの作成: %v", err)
	}
	if err := os.Chmod(identFile, 0o600); err != nil { // Windowsでは実質no-op
		t.Fatalf("chmod: %v", err)
	}

	cfgFile := filepath.Join(dir, "models.yaml")
	cfgYAML := `default_model: pi-test
models:
    pi-test:
        provider: ollamacloud
        model: gpt-oss:120b
providers:
    openai:
        base_url: https://api.openai.com/v1
    ollamacloud:
        base_url: https://ollama.com/v1
    anthropic:
        base_url: https://api.anthropic.com/v1
    openrouter:
        base_url: https://openrouter.ai/api/v1
    groq:
        base_url: https://api.groq.com/openai/v1
    nvidia:
        base_url: https://integrate.api.nvidia.com/v1
`
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("configの作成: %v", err)
	}

	sec := &secrets.Secrets{
		Version:    1,
		GatewayKey: "gk-runtime-build-0001",
		APIKeys: map[string]string{
			"openai":      "sk-openai-dummy",
			"ollamacloud": "sk-ollama-dummy",
			"anthropic":   "sk-ant-dummy",
			"openrouter":  "sk-or-dummy",
			"groq":        "sk-groq-dummy",
			"nvidia":      "sk-nv-dummy",
		},
	}
	secretsFile := writeTestSecrets(t, dir, id, sec)

	app := &appEnv{
		configFile:  cfgFile,
		secretsFile: secretsFile,
		ident:       identitySource{kind: "file", file: identFile},
	}
	rt, err := buildRuntime(slog.Default(), app)
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}

	// ウィザードcatalogの全enabled providerがregistryに接続されていること
	for _, pid := range wizard.EnabledIDs() {
		a, ok := rt.Registry.Get(pid)
		if !ok {
			t.Errorf("provider %q がregistryにありません(カタログとbuildRuntimeの不整合)", pid)
			continue
		}
		if a.Name() != pid {
			t.Errorf("provider %q のregistryキー = %q", pid, a.Name())
		}
	}
	// anthropic(カタログではDisabled)もランタイムは受理する(501スタブ)
	if _, ok := rt.Registry.Get(providers.ProviderAnthropic); !ok {
		t.Errorf("provider %q がregistryにありません", providers.ProviderAnthropic)
	}

	// 未知のproviderは従来通りfail-closedであること
	bad := filepath.Join(dir, "models-bad.yaml")
	if err := os.WriteFile(bad, []byte("default_model: pi-x\nmodels:\n    pi-x:\n        provider: unknown-prov\n        model: whatever\nproviders:\n    unknown-prov:\n        base_url: https://example.com/v1\n"), 0o644); err != nil {
		t.Fatalf("configの作成: %v", err)
	}
	_, err = buildRuntime(slog.Default(), &appEnv{
		configFile:  bad,
		secretsFile: secretsFile,
		ident:       identitySource{kind: "file", file: identFile},
	})
	if err == nil {
		t.Fatal("未知のprovider入りconfigが受理されてしまった(fail-closedのはず)")
	}
	if !strings.Contains(err.Error(), "未知のprovider") {
		t.Fatalf("想定外のエラー文言: %v", err)
	}
}
