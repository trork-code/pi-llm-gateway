// Package secrets はsecrets.yaml.age(age暗号化済み)をメモリ上で復号する。
// 平文はディスクに書き出さず、メモリ上で用が済んだバッファはゼロ化する(ベストエフォート)。
//
// APIキー保護の多層防御(アーキテクチャ仕様書§7.5)との対応:
//   - ① identity自体を age -p で暗号化している場合、AGE_PASSPHRASEで復号できる
//   - ② identityはファイルだけでなく、シークレットマネージャから注入された
//     環境変数(AGE_IDENTITY)経由でも渡せる(呼び出し側がio.Readerで渡す)
//   - ③ 暗号化secretsファイルが書き換え可能な状態なら起動を拒否する
//   - ④ 復号済み平文のバッファは解析後すぐにゼロ化する
package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
	"gopkg.in/yaml.v3"

	"github.com/trork-code/pi-llm-gateway/internal/hardening"
)

// Secrets は復号後の実キー一式。プロセスメモリ上にのみ存在する。
type Secrets struct {
	Version            int               `yaml:"version"`      // 将来のスキーマ変更に備えた管理番号(未対応の値は拒否)
	GatewayKey         string            `yaml:"gateway_key"`  // 単一キー形式
	GatewayKeys        []string          `yaml:"gateway_keys"` // 複数キー(将来のキーごとアクセス制御に向けた余地)
	APIKeys            map[string]string `yaml:"api_keys"`     // provider名 → 実APIキー
	IdentityRecipients []string          `yaml:"-"`            // 使用中identityの公開鍵(ログ・検証用。非秘匿。Loadが設定する)
}

// AllGatewayKeys は単一/複数の両形式を吸収して有効なgatewayキー一覧を返す。
func (s *Secrets) AllGatewayKeys() []string {
	keys := make([]string, 0, len(s.GatewayKeys)+1)
	if s.GatewayKey != "" {
		keys = append(keys, s.GatewayKey)
	}
	return append(keys, s.GatewayKeys...)
}

// Zero は保持している鍵のゼロ化を試みる(ベストエフォート)。
// 注: Goの文字列はイミュータブルでGCがコピーを持ち回るため完全な消去は不可能。
// 長命な参照を早く切って、メモリダンプ・検査時に鍵が見える期間を短くする効果を狙う。
func (s *Secrets) Zero() {
	s.Version = 0
	s.GatewayKey = ""
	for i := range s.GatewayKeys {
		s.GatewayKeys[i] = ""
	}
	s.GatewayKeys = nil
	for k := range s.APIKeys {
		s.APIKeys[k] = ""
	}
	s.APIKeys = nil
	s.IdentityRecipients = nil
}

// LoadOptions はsecrets.yaml.ageの復号に必要な入力。
type LoadOptions struct {
	EncryptedPath  string    // 暗号化secrets(secrets.yaml.age)のパス
	Identity       io.Reader // 秘密鍵。平文のidentity、または age -p で暗号化されたidentityファイルの内容
	Passphrase     string    // Identity自体がage暗号化されている場合のパスフレーズ
	DecryptCommand string    // 指定時: 復号を外部コマンド(age CLI等、YubiKeyプラグイン連携可)に委譲。identity/passphraseは不要
}

// Load は暗号化されたsecretsを復号してSecretsを返す。
// 復号結果(平文)はディスクに書き出さず、解析後のバッファはゼロ化する。
func Load(opts LoadOptions) (*Secrets, error) {
	if opts.EncryptedPath == "" {
		return nil, errors.New("暗号化secretsのパスが空です")
	}
	// 多層防御③: 暗号化済みファイルでも書き換え可能な状態は拒否する
	if err := hardening.CheckNotWorldWritable(opts.EncryptedPath); err != nil {
		return nil, err
	}

	var data []byte
	var identities []age.Identity

	if opts.DecryptCommand != "" {
		// 外部コマンド復号バックエンド(age CLI等)。identityはコマンド側で解決される
		// (YubiKeyプラグインなど、鍵がハードウェアに留まる形にも対応)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := runCommandOutput(ctx, opts.DecryptCommand)
		if err != nil {
			return nil, fmt.Errorf("外部復号コマンドに失敗: %w", err)
		}
		data = d
	} else {
		if opts.Identity == nil {
			return nil, errors.New("identityが指定されていません")
		}
		idData, err := io.ReadAll(io.LimitReader(opts.Identity, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("identityの読み取りに失敗: %w", err)
		}
		identities, err = parseIdentities(idData, opts.Passphrase)
		wipe(idData) // 秘密鍵の生バイトは用が済みしだい消す(ベストエフォート)
		if err != nil {
			return nil, err
		}

		ef, err := os.Open(opts.EncryptedPath)
		if err != nil {
			return nil, fmt.Errorf("暗号化secretsを開けません(%s): %w", opts.EncryptedPath, err)
		}
		defer ef.Close()
		efData, err := io.ReadAll(io.LimitReader(ef, 16<<20))
		if err != nil {
			return nil, fmt.Errorf("暗号化secretsの読み取りに失敗: %w", err)
		}
		// armor形式(age -a)とbinary形式の両方に対応する
		var src io.Reader = bytes.NewReader(efData)
		if strings.HasPrefix(strings.TrimSpace(string(efData)), "-----BEGIN AGE ENCRYPTED FILE-----") {
			src = armor.NewReader(src)
		}
		plain, err := age.Decrypt(src, identities...)
		if err != nil {
			return nil, fmt.Errorf("age復号に失敗(秘密鍵が一致しない可能性): %w", err)
		}
		d, err := io.ReadAll(io.LimitReader(plain, 16<<20))
		if err != nil {
			return nil, fmt.Errorf("復号データの読み取りに失敗: %w", err)
		}
		wipe(efData) // 暗号化データは読み終えたので消せる(ベストエフォート)
		data = d
	}

	var s Secrets
	err := yaml.Unmarshal(data, &s)
	wipe(data) // 平文yamlは解析後すぐに消す(ベストエフォート)
	if err != nil {
		return nil, fmt.Errorf("復号後YAMLの解析に失敗: %w", err)
	}
	// スキーマの将来変更に備えたバージョン管理(未指定は0=version 1扱いで互換維持)
	if s.Version > 1 {
		return nil, fmt.Errorf("secrets.yaml の version %d は未対応です(対応: 1)", s.Version)
	}
	s.IdentityRecipients = publicRecipients(identities)
	return &s, nil
}

// publicRecipients はidentityの公開鍵(非秘匿・ログ表示用)を取り出す。
// 運用者が「どの鍵でgatewayが動いているか」を、鍵そのものを晒さずに確認できるようにする。
func publicRecipients(ids []age.Identity) []string {
	var out []string
	for _, id := range ids {
		if xi, ok := id.(interface{ Recipient() *age.X25519Recipient }); ok {
			out = append(out, xi.Recipient().String())
		}
	}
	return out
}

// parseIdentities はidentityファイルの内容からage.Identity一覧を取り出す。
// identityファイル自体が age -p で暗号化されている場合(armor形式またはbinary形式)、
// passphraseを使ってまずメモリ上で復号する。
func parseIdentities(data []byte, passphrase string) ([]age.Identity, error) {
	// まず素のidentity(AGE-SECRET-KEY-...)として解析を試みる
	if ids, err := age.ParseIdentities(bytes.NewReader(data)); err == nil {
		return ids, nil
	} else if passphrase == "" {
		return nil, fmt.Errorf("identityの解析に失敗(パスフレーズ保護されたidentityの場合はAGE_PASSPHRASEを設定してください): %w", err)
	}

	// age -p で暗号化されたidentityファイルとみなして復号する
	src := io.Reader(bytes.NewReader(data))
	if strings.HasPrefix(strings.TrimSpace(string(data)), "-----BEGIN AGE ENCRYPTED FILE-----") {
		src = armor.NewReader(src)
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("パスフレーズidentityの構築に失敗: %w", err)
	}
	plain, err := age.Decrypt(src, id)
	if err != nil {
		return nil, fmt.Errorf("identityの復号に失敗(AGE_PASSPHRASEを確認してください): %w", err)
	}
	ids, err := age.ParseIdentities(plain)
	if err != nil {
		return nil, fmt.Errorf("復号後identityの解析に失敗: %w", err)
	}
	return ids, nil
}

// wipe はバイト列をゼロで上書きする(ベストエフォート)。
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
