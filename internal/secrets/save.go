// 編集済みSecretsの再暗号化・保存。
// `gateway keys` サブコマンド(config/gateway/keys_cmd.go)から使う。
// 平文は一時バッファにしか存在させず、書き出し先は常にage暗号化(armor形式)とする。
package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
	"gopkg.in/yaml.v3"
)

// Save は Secrets を recipients(age1... 公開鍵)宛てに age暗号化(armor形式)して
// path に原子的に書き戻す。
//
// 規約:
//   - version が未指定(0, 従来の平文時代のファイル)の場合は 1 に昇格して書き出す
//   - 一時ファイル(同一ディレクトリ, 0600)に書いた後 rename するため、
//     稼働中サーバーが読み取り中でも断片的なファイルは観測されない
//   - 出力は常にarmor形式(Loadはarmor/binary両方に対応)
func Save(path string, s *Secrets, recipients []string) error {
	recs := make([]age.Recipient, 0, len(recipients))
	for i, r := range recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		rc, err := age.ParseX25519Recipient(r)
		if err != nil {
			return fmt.Errorf("recipient #%d (%s) を解釈できません: %w", i, r, err)
		}
		recs = append(recs, rc)
	}
	if len(recs) == 0 {
		return errors.New("再暗号化に必要な recipient(公開鍵 age1... )がありません")
	}
	if s.Version > 1 {
		return fmt.Errorf("secrets.yaml の version %d は未対応です(対応: 1)", s.Version)
	}
	if s.Version == 0 {
		// 未指定(=version 1扱いのレガシー)からは version 1 に昇格して明示する
		s.Version = 1
	}

	plain, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("secrets YAMLの生成に失敗: %w", err)
	}
	defer wipe(plain) // 平文は用が済みしだい消す(ベストエフォート)

	// age暗号化 → armor
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	enc, err := age.Encrypt(aw, recs...)
	if err != nil {
		return fmt.Errorf("age暗号化に失敗: %w", err)
	}
	if _, err := enc.Write(plain); err != nil {
		return fmt.Errorf("secrets平文の書き込みに失敗: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("age暗号化の完成に失敗: %w", err)
	}
	if err := aw.Close(); err != nil {
		return fmt.Errorf("armorの完成に失敗: %w", err)
	}

	// 原子的書き戻し: 同一ディレクトリに 0600 で一時ファイル → rename
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".secrets-*.age")
	if err != nil {
		return fmt.Errorf("一時ファイルの作成に失敗(%s): %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("一時ファイルへの書き込みに失敗: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("一時ファイルのflushに失敗: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("一時ファイルを閉じられません: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("secretsの差し替えに失敗(%s → %s): %w", tmpName, path, err)
	}
	return nil
}
