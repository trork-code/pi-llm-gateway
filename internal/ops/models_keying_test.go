package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUpsertModelAndFindModel は実モデル名キー(pi id=実モデル名の新規約)の書込と衝突検知。
func TestUpsertModelAndFindModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(
		"default_model: minimax-m3.1-flash\nmodels:\n  minimax-m3.1-flash:\n    provider: minimax\nproviders:\n  minimax:\n    base_url: https://api.minimax.io/v1\n  ollamacloud:\n    base_url: https://ollama.com/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 実モデル名キーの1件目
	if err := UpsertModel(path, "minimax", "minimax-m3.1-flash", true); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	owner, err := FindModel(path, "minimax-m3.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || owner.Provider != "minimax" || owner.Key != "minimax-m3.1-flash" {
		t.Fatalf("owner = %+v", owner)
	}

	// 別providerへの割当変更 = 同キー(実モデル名)の上書き → メニューの確認フローで担保
	if err := UpsertModel(path, "ollamacloud", "minimax-m3.1-flash", false); err != nil {
		t.Fatalf("UpsertModel(別provider): %v", err)
	}
	owner, err = FindModel(path, "minimax-m3.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || owner.Provider != "ollamacloud" || owner.Key != "minimax-m3.1-flash" {
		t.Fatalf("owner after reassign = %+v", owner)
	}

	// legacyエイリアス(キー != 実モデル)を重ねてもFindModelは実モデル名で見つける
	legacy := filepath.Join(dir, "legacy.yaml")
	if err := os.WriteFile(legacy, []byte(
		"default_model: pi-glm\nmodels:\n  pi-glm:\n    provider: ollamacloud\n    model: glm-5.3-flash:cloud\nproviders:\n  ollamacloud:\n    base_url: https://ollama.com/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	owner, err = FindModel(legacy, "glm-5.3-flash:cloud")
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || owner.Key != "pi-glm" || owner.Provider != "ollamacloud" {
		t.Fatalf("legacy find = %+v", owner)
	}
}

// TestUpsertModelValidation は空引数の拒否。
func TestUpsertModelValidation(t *testing.T) {
	if err := UpsertModel(filepath.Join(t.TempDir(), "m.yaml"), "", "x", false); err == nil {
		t.Fatal("empty provider must fail")
	}
	if err := UpsertModel(filepath.Join(t.TempDir(), "m.yaml"), "ollamacloud", "", false); err == nil {
		t.Fatal("empty model must fail")
	}
}
