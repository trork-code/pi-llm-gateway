package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validYAML = `
default_model: pi-default

models:
  pi-default:
    provider: anthropic
    model: claude-sonnet-4-6
  pi-fast:
    provider: openai
    model: gpt-4.1-mini

providers:
  openai:
    base_url: https://api.openai.com/v1
  anthropic:
    base_url: https://api.anthropic.com/v1
`

func TestLoad_OK(t *testing.T) {
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	mc, ok := cfg.Resolve("pi-fast")
	if !ok || mc.Provider != "openai" || mc.Model != "gpt-4.1-mini" {
		t.Fatalf("Resolve(pi-fast) = %+v, ok = %v", mc, ok)
	}
	if cfg.Models["pi-default"].Model != "claude-sonnet-4-6" {
		t.Fatalf("pi-default = %+v", cfg.Models["pi-default"])
	}
}

func TestLoad_UnknownProvider(t *testing.T) {
	bad := `
default_model: a
models:
  a:
    provider: missing
    model: x
providers: {}
`
	if _, err := Load(writeTemp(t, bad)); err == nil {
		t.Fatal("存在しないprovider参照が弾かれていません")
	}
}

func TestLoad_DefaultModelMissing(t *testing.T) {
	bad := `
default_model: nope
models:
  a:
    provider: openai
    model: x
providers:
  openai:
    base_url: https://api.openai.com/v1
`
	if _, err := Load(writeTemp(t, bad)); err == nil {
		t.Fatal("存在しないdefault_modelが弾かれていません")
	}
}

// --- ResolveRequest(実モデル名キーの新規約) -----------------------------------

const mixedYAML = `
default_model: minimax-m3.1-flash
models:
  minimax-m3.1-flash:
    provider: minimax
  pi-glm:
    provider: ollamacloud
    model: glm-5.3-flash:cloud
  glm-5.3-flash:cloud:
    provider: ollamacloud
providers:
  minimax:
    base_url: https://api.minimax.io/v1
  ollamacloud:
    base_url: https://ollama.com/v1
`

// 実モデル名キーで解決できる(pi id = 実モデル名)。legacyエイリアスも先に完全一致で受理。
func TestResolveRequest(t *testing.T) {
	cfg, err := Load(writeTemp(t, mixedYAML))
	if err != nil {
		t.Fatal(err)
	}
	if mc, ok := cfg.ResolveRequest("minimax-m3.1-flash"); !ok || mc.Provider != "minimax" {
		t.Errorf("model-name key = %+v, %v", mc, ok)
	}
	if mc, ok := cfg.ResolveRequest("glm-5.3-flash:cloud"); !ok || mc.Provider != "ollamacloud" {
		t.Errorf("model-name key(legacyと同一名) = %+v, %v", mc, ok)
	}
	// legacy: エイリアスキー完全一致が優先(既存セッションの互換)
	if mc, ok := cfg.ResolveRequest("pi-glm"); !ok || mc.Model != "glm-5.3-flash:cloud" {
		t.Errorf("legacy alias = %+v, %v", mc, ok)
	}
	if _, ok := cfg.ResolveRequest("nope"); ok {
		t.Error("unknown id must not resolve")
	}
}

func TestModelNameSetAndLegacy(t *testing.T) {
	cfg, err := Load(writeTemp(t, mixedYAML))
	if err != nil {
		t.Fatal(err)
	}
	names := cfg.ModelNameSet()
	if len(names) != 2 || names[0] != "glm-5.3-flash:cloud" || names[1] != "minimax-m3.1-flash" {
		t.Errorf("ModelNameSet = %v", names)
	}
	if legacy := cfg.LegacyAliases(); len(legacy) != 1 || legacy[0] != "pi-glm" {
		t.Errorf("LegacyAliases = %v", legacy)
	}
	if owners := cfg.FindOwnersOfModel("glm-5.3-flash:cloud"); len(owners) != 2 || owners[0] != "glm-5.3-flash:cloud" || owners[1] != "pi-glm" {
		t.Errorf("FindOwnersOfModel = %v", owners)
	}
}
