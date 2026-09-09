package secrets

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
)

const testYAML = `gateway_key: gk-test
api_keys:
  openai: sk-test-openai
  ollamacloud: ollama-test-key
`

func encryptTo(t *testing.T, recipient age.Recipient, plaintext []byte) []byte {
	t.Helper()
	var enc bytes.Buffer
	aw := armor.NewWriter(&enc)
	w, err := age.Encrypt(aw, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
	return enc.Bytes()
}

func writeTemp(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secrets.yaml.age")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_PlainIdentity(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, encryptTo(t, id.Recipient(), []byte(testYAML)))

	sec, err := Load(LoadOptions{
		EncryptedPath: path,
		Identity:      strings.NewReader(id.String()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if sec.GatewayKey != "gk-test" {
		t.Fatalf("gateway_key = %q", sec.GatewayKey)
	}
	if sec.APIKeys["ollamacloud"] != "ollama-test-key" {
		t.Fatalf("api_keys = %v", sec.APIKeys)
	}
	// identityの公開鍵(非秘匿)が取り出されていること(運用時の可視化用)
	if len(sec.IdentityRecipients) != 1 || !strings.HasPrefix(sec.IdentityRecipients[0], "age1") {
		t.Fatalf("IdentityRecipients = %v", sec.IdentityRecipients)
	}
	// version未指定は互換のためversion 1扱い
	if sec.Version != 0 {
		t.Fatalf("version = %d", sec.Version)
	}
}

func TestLoad_UnknownVersion(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, encryptTo(t, id.Recipient(), []byte("version: 2\ngateway_key: x\n")))
	if _, err := Load(LoadOptions{
		EncryptedPath: path,
		Identity:      strings.NewReader(id.String()),
	}); err == nil {
		t.Fatal("未対応のversionが受け入れられました")
	}
}

func TestLoad_PassphraseProtectedIdentity(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	// identity自体を age -p 相当で暗号化する(平文の鍵がディスクに落ちない形)
	var idEnc bytes.Buffer
	aw := armor.NewWriter(&idEnc)
	recipient, err := age.NewScryptRecipient("test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	w, err := age.Encrypt(aw, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(w, id.String()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}

	path := writeTemp(t, encryptTo(t, id.Recipient(), []byte(testYAML)))

	sec, err := Load(LoadOptions{
		EncryptedPath: path,
		Identity:      bytes.NewReader(idEnc.Bytes()),
		Passphrase:    "test-passphrase",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sec.APIKeys["openai"] != "sk-test-openai" {
		t.Fatalf("api_keys = %v", sec.APIKeys)
	}

	// 間違ったパスフレーズでは復号できない
	if _, err := Load(LoadOptions{
		EncryptedPath: path,
		Identity:      bytes.NewReader(idEnc.Bytes()),
		Passphrase:    "wrong-passphrase",
	}); err == nil {
		t.Fatal("誤ったパスフレーズが受け入れられました")
	}
}

func TestAllGatewayKeys(t *testing.T) {
	s := &Secrets{GatewayKey: "a", GatewayKeys: []string{"b", "c"}}
	got := s.AllGatewayKeys()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("AllGatewayKeys() = %v", got)
	}
	s.Zero()
	if s.GatewayKey != "" || s.GatewayKeys != nil || s.APIKeys != nil {
		t.Fatal("Zero()後も鍵が残っています")
	}
}
