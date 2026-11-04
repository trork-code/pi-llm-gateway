// secrets.yaml.age の鍵ローテーション — scripts/rotate-secrets.sh のGo化(Windows対応)。
//
// bash版の流れ(復号 → 編集 → 再暗号化 → 復号ラウンドトリップ検証 → 置き換え)のうち、
// Go版が担うのは「受信者(recipient)の付け替えとファイル更新」+「検証」。
// 鍵値そのものの編集は `gateway keys set/unset` が担う(役割分担)。
//
// 安全規約(fail-closed):
//   - 置き換え前に .bak へ退避(暗号化済みなので残置は安全)
//   - 更新後に同一入力で復可ラウンドトリップ検証。失敗なら旧ファイルへ復元
//   - identity自体をローテーションする場合のみ SkipVerify(=bashの--skip-verify)
//   - SECRETS_DECRYPT_CMD経由(identity系envが無い)は -recipient 必須でfail-closed
package ops

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// RotateOptions は回転の入力。
type RotateOptions struct {
	// Path は secrets.yaml.age のパス。
	Path string
	// Sec は復号済み(回転前)のsecrets。
	Sec *secrets.Secrets
	// Recipients は再暗号化先の公開鍵。空なら Sec.IdentityRecipients(identityモード)
	// を既定に使う。identity recipients も無ければ失敗(fail-closed)。
	Recipients []string
	// Reload は保存後のラウンドトリップ検証用: 同一入力での再復号。
	// nil または SkipVerify=true なら検証しない。
	Reload func() (*secrets.Secrets, error)
	// SkipVerify はidentity自体のローテーション時のみtrue(現鍵では新しい
	// ファイルを復号できないため検証を省略する=bash--skip-verifyと同一)。
	SkipVerify bool
}

// RotateResult は実行結果。
type RotateResult struct {
	BackupPath string
	Recipients []string // 実際に使ったrecipient
	Verified   bool     // ラウンドトリップ検証に通ったか
}

// RotateSecrets はsecrets.yaml.ageを指定recipientで再暗号化して置き換える。
// 失敗時は旧ファイル(.bak退避済み)を復元してエラーを返す。
func RotateSecrets(opts RotateOptions) (RotateResult, error) {
	if opts.Path == "" {
		return RotateResult{}, errors.New("secrets.yaml.ageのパスが空です")
	}
	if opts.Sec == nil {
		return RotateResult{}, errors.New("復号済みsecretsがありません(先に復可に成功してください)")
	}

	// 1. 受信者確定(明示 > identity recipients > 拒否)
	recips := normalizeRecipients(opts.Recipients)
	if len(recips) == 0 {
		recips = normalizeRecipients(opts.Sec.IdentityRecipients)
	}
	if len(recips) == 0 {
		return RotateResult{}, errors.New("再暗号化先recipientが未確定です(identityで復号していない場合は -recipient <age1...> を指定してください)")
	}
	for _, r := range recips {
		// 形式検証: X25519(age1…)/plugin受信者を ParseRecipients で通す。
		// 不正recipientは暗号化前にここで落とす(fail-closed)
		if _, err := age.ParseRecipients(strings.NewReader(r)); err != nil {
			return RotateResult{}, fmt.Errorf("recipient %q が不正です: %w", maskRecipient(r), err)
		}
	}

	// 2. .bak退避(暗号化済みファイルなので残置安全。bash版と同一の単一.bak)
	orig, err := os.ReadFile(opts.Path)
	if err != nil {
		return RotateResult{}, fmt.Errorf("%sを読めません: %w", opts.Path, err)
	}
	bak := opts.Path + ".bak"
	if err := os.WriteFile(bak, orig, 0o600); err != nil {
		return RotateResult{}, fmt.Errorf("バックアップ作成に失敗(%s): %w", bak, err)
	}

	// 3. 再暗号化+原子的差し替え(secrets.Save: armor + 0600 temp → rename)
	if err := secrets.Save(opts.Path, opts.Sec, recips); err != nil {
		_ = restoreRotate(opts.Path, orig)
		return RotateResult{}, fmt.Errorf("再暗号化に失敗したため旧ファイルに復元しました: %w", err)
	}

	// 4. ラウンドトリップ検証(同一入力で復可→内容一致) してから成功扱い
	verified := false
	if !opts.SkipVerify && opts.Reload != nil {
		fresh, err := opts.Reload()
		if err != nil {
			_ = restoreRotate(opts.Path, orig)
			return RotateResult{}, fmt.Errorf("検証失敗: 新しい%sを現在の入力で復号できません(旧ファイルに復元済み)。identityもローテーションする場合は --skip-verify を付け、新しいidentityでの復号確認を先に行ってください: %w", opts.Path, err)
		}
		if !secretsEqual(opts.Sec, fresh) {
			_ = restoreRotate(opts.Path, orig)
			return RotateResult{}, errors.New("検証失敗: 回転前後でsecretsの内容が一致しません(旧ファイルに復元済み)")
		}
		verified = true
	}
	return RotateResult{BackupPath: bak, Recipients: recips, Verified: verified}, nil
}

// restoreRotate は旧ファイルを復元する(最善努力。失敗は呼び出し側のエラーで伝える)。
func restoreRotate(path string, orig []byte) error {
	return setup.AtomicBytes(path, orig, 0o600)
}

func normalizeRecipients(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, r := range list {
		r = trimSpaceAll(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func trimSpaceAll(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// secretsEqual は回転前後の内容一致(鍵一式は同一のはず。 YAML順序には依存しない)。
func secretsEqual(a, b *secrets.Secrets) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Version != b.Version {
		return false
	}
	if !equalStringSlices(a.AllGatewayKeys(), b.AllGatewayKeys()) {
		return false
	}
	if len(a.APIKeys) != len(b.APIKeys) {
		return false
	}
	for k, v := range a.APIKeys {
		if bv, ok := b.APIKeys[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// maskRecipient はエラーメッセージ用にrecipientを先頭だけ見せる(非秘匿だが保険)。
func maskRecipient(r string) string {
	if len(r) > 12 {
		return r[:12] + "…"
	}
	return r
}
