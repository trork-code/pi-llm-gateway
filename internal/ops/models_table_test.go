package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListAliases はconfig読み・既定マーク・ソートを確認する。
func TestListAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	// b, a の順で書くが結果はソートされる(aが既定)
	content := `default_model: b
models:
  b:
    provider: ollamacloud
    model: glm-5.3-flash:cloud
  a:
    provider: ollamacloud
    model: gpt-oss:120b
providers:
  ollamacloud:
    base_url: https://ollama.com/v1
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := ListAliases(path)
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Alias != "a" || rows[1].Alias != "b" {
		t.Errorf("not sorted: %s, %s", rows[0].Alias, rows[1].Alias)
	}
	if !rows[1].Default { // default_model: b
		t.Errorf("default should be b, got %v/%v", rows[0].Default, rows[1].Default)
	}
}

// TestListAliasesMissing は存在しないconfigはエラー。
func TestListAliasesMissing(t *testing.T) {
	if _, err := ListAliases(filepath.Join(t.TempDir(), "none.yaml")); err == nil {
		t.Fatal("missing config must fail")
	}
}

// TestPrintModelsTable は*印とヘッダを確認する。
func TestPrintModelsTable(t *testing.T) {
	var b strings.Builder
	PrintModelsTable(&b, "config.yaml", []AliasRow{
		{Alias: "b", Provider: "ollamacloud", Model: "glm-5.3-flash:cloud"},
		{Alias: "a", Provider: "ollamacloud", Model: "gpt-oss:120b", Default: true},
	})
	out := b.String()
	for _, want := range []string{"config.yaml", "*", "a", "ollamacloud", "gpt-oss:120b"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q not found in:\n%s", want, out)
		}
	}
}
