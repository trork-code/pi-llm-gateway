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
