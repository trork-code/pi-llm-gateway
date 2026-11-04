package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// TestMenuDoctor はメニュー「6) Diagnose」が隠しCLI doctorと同一コア(ops.Doctor)で
// 動き、レポートを出力すること(実環境は触らない)。
func TestMenuDoctor(t *testing.T) {
	dir := t.TempDir()
	paths := setup.Layout(dir)
	if err := os.MkdirAll(filepath.Dir(paths.Identity), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Identity, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte(
		"default_model: a1\nmodels:\n  a1:\n    provider: ollamacloud\n    model: m\nproviders:\n  ollamacloud:\n    base_url: https://ollama.com/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setup.SaveState(paths, &setup.State{Version: 1, Port: 18400,
		Bind: "127.0.0.1", Startup: "manual", IdentityMode: "file"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	t.Setenv("PORT", "") // メニュー/doctorのポート解決をsetup.jsonに寄せる

	var out strings.Builder
	if err := menuDoctor(paths, &out); err != nil {
		t.Fatalf("menuDoctor: %v", err)
	}
	s := out.String()
	for _, want := range []string{"pi-llm-gateway doctor", "config/models.yaml", "result:"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q not found in:\n%s", want, s)
		}
	}
}

// TestMenuDoctorNoSecrets はsecretsが読めない環境でもdoctorはfail行として表示し、
// エラーで全体が止まらないこと。
func TestMenuDoctorNoSecrets(t *testing.T) {
	dir := t.TempDir()
	paths := setup.Layout(dir)
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	var out strings.Builder
	if err := menuDoctor(paths, &out); err != nil {
		t.Fatalf("menuDoctor: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "[fail]  1.") {
		t.Errorf("item1 should fail on empty dir:\n%s", s)
	}
	if !strings.Contains(s, "[fail]  3.") {
		t.Errorf("item3 should fail without identity:\n%s", s)
	}
}
