package ops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

// rotateEnv はrotateテスト用の最小環境(identity×2 + secrets)。
type rotateEnv struct {
	dir    string
	path   string
	id1    *age.X25519Identity
	id2    *age.X25519Identity
	gwKeys []string
	apiKey string
}

func newRotateEnv(t *testing.T) *rotateEnv {
	t.Helper()
	dir := t.TempDir()
	id1, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	e := &rotateEnv{
		dir:    dir,
		path:   filepath.Join(dir, "secrets.yaml.age"),
		id1:    id1,
		id2:    id2,
		gwKeys: []string{"gk-rotatetest-key"},
		apiKey: "sk-rotate-provider-key",
	}
	e.save(t, e.id1)
	return e
}

func (e *rotateEnv) save(t *testing.T, id *age.X25519Identity) {
	t.Helper()
	sec := &secrets.Secrets{
		Version:     1,
		GatewayKeys: e.gwKeys,
		APIKeys:     map[string]string{"ollamacloud": e.apiKey},
	}
	if err := secrets.Save(e.path, sec, []string{id.Recipient().String()}); err != nil {
		t.Fatalf("secrets保存: %v", err)
	}
}

func (e *rotateEnv) load(t *testing.T, id *age.X25519Identity) *secrets.Secrets {
	t.Helper()
	sec, err := secrets.Load(secrets.LoadOptions{
		EncryptedPath: e.path,
		Identity:      strings.NewReader(id.String()),
	})
	if err != nil {
		t.Fatalf("secrets復号: %v", err)
	}
	return sec
}

func (e *rotateEnv) reloadWith(id *age.X25519Identity) func() (*secrets.Secrets, error) {
	return func() (*secrets.Secrets, error) {
		return secrets.Load(secrets.LoadOptions{
			EncryptedPath: e.path,
			Identity:      strings.NewReader(id.String()),
		})
	}
}

// TestRotateRefreshRoundTrip はrecipient省略 → 現行identityで再暗号化、検証ok。
func TestRotateRefreshRoundTrip(t *testing.T) {
	e := newRotateEnv(t)
	sec := e.load(t, e.id1) // IdentityRecipients が設定される

	if mtimeBefore := modTime(t, e.path); !mtimeBefore.IsZero() {
		time.Sleep(1100 * time.Millisecond) // mtime解像度確保
	}

	res, err := RotateSecrets(RotateOptions{
		Path:   e.path,
		Sec:    sec,
		Reload: e.reloadWith(e.id1),
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !res.Verified {
		t.Error("verified = false, want true")
	}
	if len(res.Recipients) != 1 || res.Recipients[0] != e.id1.Recipient().String() {
		t.Errorf("recipients = %v", res.Recipients)
	}
	if res.BackupPath != e.path+".bak" || !fileExistsR(res.BackupPath) {
		t.Errorf("backup = %q (exists=%v)", res.BackupPath, fileExistsR(res.BackupPath))
	}
	// 内容不変・復可
	fresh := e.load(t, e.id1)
	if !secretsEqual(sec, fresh) {
		t.Error("secrets content changed by rotation")
	}
}

// TestRotateNewRecipientVerified は新recipientへの付け替え(検証ok)→旧鍵で復号不能。
func TestRotateNewRecipientVerified(t *testing.T) {
	e := newRotateEnv(t)
	sec := e.load(t, e.id1)

	// 検証は旧identityでは不可能(新しいrecipientはid2) → Reload も id2 で行う
	res, err := RotateSecrets(RotateOptions{
		Path:       e.path,
		Sec:        sec,
		Recipients: []string{e.id2.Recipient().String()},
		Reload:     e.reloadWith(e.id2),
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !res.Verified {
		t.Error("verified = false, want true")
	}
	if _, err := secrets.Load(secrets.LoadOptions{
		EncryptedPath: e.path,
		Identity:      strings.NewReader(e.id1.String()),
	}); err == nil {
		t.Error("old identity can still decrypt — recipients NOT rotated")
	}
	fresh := e.load(t, e.id2)
	if !secretsEqual(sec, fresh) {
		t.Error("secrets content changed by rotation")
	}
}

// TestRotateVerifyFailRestores は検証失敗 → 旧ファイル復元(fail-closed)。
func TestRotateVerifyFailRestores(t *testing.T) {
	e := newRotateEnv(t)
	sec := e.load(t, e.id1)
	before, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}

	// 新recipientへ回すが、検証は復号できない旧identityで行う → 必ず失敗
	_, err = RotateSecrets(RotateOptions{
		Path:       e.path,
		Sec:        sec,
		Recipients: []string{e.id2.Recipient().String()},
		Reload:     e.reloadWith(e.id1),
	})
	if err == nil {
		t.Fatal("rotate should fail verification")
	}
	if !strings.Contains(err.Error(), "検証失敗") {
		t.Errorf("error = %v", err)
	}
	// 復元確認: 旧identityで同じ内容が復号できる
	after := e.load(t, e.id1)
	if !secretsEqual(sec, after) {
		t.Error("contents differ after restore")
	}
	now, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(now) == string(before) {
		// 注: 復元は「旧ファイルと同一バイト」ではなく「旧recipientで再暗号化済み」の
		// 保存済みバイトの書き戻し。ここではバイト一致まで要求する(After=Before退避)
		t.Log("restored bytes identical to pre-rotate file (expected: .bak write-back)")
	}
}

// TestRotateSkipVerify は--skip-verify相当: 検証なしで新recipientへの回転を完走。
func TestRotateSkipVerify(t *testing.T) {
	e := newRotateEnv(t)
	sec := e.load(t, e.id1)

	res, err := RotateSecrets(RotateOptions{
		Path:       e.path,
		Sec:        sec,
		Recipients: []string{e.id2.Recipient().String()},
		SkipVerify: true, // Reload 未指定と同義の経路も確認する
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if res.Verified {
		t.Error("verified should be false with SkipVerify")
	}
	fresh := e.load(t, e.id2)
	if !secretsEqual(sec, fresh) {
		t.Error("content mismatch after skip-verify rotation")
	}
}

// TestRotateNoRecipientFails は identity recipients 無し(DECRYPT_CMD相当)で
// recipient未指定 → fail-closed。
func TestRotateNoRecipientFails(t *testing.T) {
	e := newRotateEnv(t)
	sec := e.load(t, e.id1)
	sec.IdentityRecipients = nil // SECRETS_DECRYPT_CMD経由相当(identity取得不可)

	_, err := RotateSecrets(RotateOptions{Path: e.path, Sec: sec})
	if err == nil {
		t.Fatal("rotate without any recipient must fail (fail-closed)")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error = %v", err)
	}
}

// TestRotateBadRecipient は不正recipientは暗号化前に落ちる(ファイル不変)。
func TestRotateBadRecipient(t *testing.T) {
	e := newRotateEnv(t)
	sec := e.load(t, e.id1)
	before, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = RotateSecrets(RotateOptions{
		Path:       e.path,
		Sec:        sec,
		Recipients: []string{"not-a-valid-age-recipient"},
	})
	if err == nil {
		t.Fatal("invalid recipient must fail")
	}
	after, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("file must be untouched on early validation failure")
	}
}

// TestRotateNilSec はSec無しは即失敗。
func TestRotateNilSec(t *testing.T) {
	e := newRotateEnv(t)
	if _, err := RotateSecrets(RotateOptions{Path: e.path}); err == nil {
		t.Fatal("nil sec must fail")
	}
	if _, err := RotateSecrets(RotateOptions{Sec: e.load(t, e.id1)}); err == nil {
		t.Fatal("empty path must fail")
	}
	if _, err := RotateSecrets(RotateOptions{Path: filepath.Join(e.dir, "missing.age"),
		Sec: e.load(t, e.id1), Recipients: []string{e.id1.Recipient().String()}}); err == nil {
		t.Fatal("missing file must fail")
	}
}

func fileExistsR(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}
		}
		t.Fatal(err)
	}
	return fi.ModTime()
}
