// Save(再暗号化・原子的書き戻し)のテスト。
package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// TestSaveRoundtripArmor は Save → Load のラウンドトリップで値が保たれることを確認する。
func TestSaveRoundtripArmor(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("identityの生成: %v", err)
	}
	orig := &Secrets{
		Version:     1,
		GatewayKey:  "gk-legacy",
		GatewayKeys: []string{"gk-extra1", "gk-extra2"},
		APIKeys: map[string]string{
			"ollamacloud": "sk-test-1234",
		},
	}

	path := filepath.Join(t.TempDir(), "secrets.yaml.age")
	if err := Save(path, orig, []string{id.Recipient().String()}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 書き出しはarmor形式
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ファイルの読み取り: %v", err)
	}
	if !strings.HasPrefix(string(raw), "-----BEGIN AGE ENCRYPTED FILE-----") {
		t.Errorf("armor形式で書き出されていません: %q", string(raw[:60]))
	}

	loaded, err := Load(LoadOptions{EncryptedPath: path, Identity: strings.NewReader(id.String())})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1", loaded.Version)
	}
	if loaded.GatewayKey != orig.GatewayKey {
		t.Errorf("GatewayKey = %q, want %q", loaded.GatewayKey, orig.GatewayKey)
	}
	if len(loaded.GatewayKeys) != 2 || loaded.GatewayKeys[0] != "gk-extra1" {
		t.Errorf("GatewayKeys = %v, want [gk-extra1 gk-extra2]", loaded.GatewayKeys)
	}
	if loaded.APIKeys["ollamacloud"] != "sk-test-1234" {
		t.Errorf("APIKeys[ollamacloud] = %q", loaded.APIKeys["ollamacloud"])
	}
	if len(loaded.IdentityRecipients) != 1 || loaded.IdentityRecipients[0] != id.Recipient().String() {
		t.Errorf("IdentityRecipients = %v", loaded.IdentityRecipients)
	}
}

// TestSaveEmptyRecipients はrecipientなしの保存を拒否する。
func TestSaveEmptyRecipients(t *testing.T) {
	sec := &Secrets{Version: 1, GatewayKey: "k"}
	if err := Save(filepath.Join(t.TempDir(), "x.age"), sec, nil); err == nil {
		t.Fatal("recipientなしでの保存が成功してしまった")
	}
}

// TestSaveRejectsBadRecipient は不正なrecipient文字列を拒否する。
func TestSaveRejectsBadRecipient(t *testing.T) {
	sec := &Secrets{Version: 1}
	err := Save(filepath.Join(t.TempDir(), "x.age"), sec, []string{"not-a-public-key"})
	if err == nil {
		t.Fatal("不正recipientが受理された")
	}
}

// TestSaveAtomicReplace は既存ファイルを置き換え、一時ファイルの残骸が残らないことを確認する。
func TestSaveAtomicReplace(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("identityの生成: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.yaml.age")

	v1 := &Secrets{Version: 1, GatewayKey: "gk-a"}
	if err := Save(path, v1, []string{id.Recipient().String()}); err != nil {
		t.Fatalf("Save(1): %v", err)
	}
	v2 := &Secrets{Version: 1, GatewayKey: "gk-a", GatewayKeys: []string{"gk-b"}}
	if err := Save(path, v2, []string{id.Recipient().String()}); err != nil {
		t.Fatalf("Save(2): %v", err)
	}
	loaded, err := Load(LoadOptions{EncryptedPath: path, Identity: strings.NewReader(id.String())})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.GatewayKeys) != 1 || loaded.GatewayKeys[0] != "gk-b" {
		t.Errorf("GatewayKeys = %v, want [gk-b]", loaded.GatewayKeys)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".secrets-") {
			t.Errorf("一時ファイルが残っています: %s", e.Name())
		}
	}
}

// TestSaveLegacyVersionBump は version 0(レガシー)からの保存で version 1 に昇格することを確認。
func TestSaveLegacyVersionBump(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("identityの生成: %v", err)
	}
	sec := &Secrets{GatewayKeys: []string{"gk-a"}} // Version=0(レガシー)
	path := filepath.Join(t.TempDir(), "secrets.yaml.age")
	if err := Save(path, sec, []string{id.Recipient().String()}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(LoadOptions{EncryptedPath: path, Identity: strings.NewReader(id.String())})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1", loaded.Version)
	}
}
