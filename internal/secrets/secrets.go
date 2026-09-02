// Package secrets はsecrets.yaml.age(age暗号化済み)をメモリ上で復号する。
// 平文はディスクに書き出さない。
package secrets

import (
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	"gopkg.in/yaml.v3"
)

// Secrets は復号後の実キー一式。メモリ上にのみ存在する。
type Secrets struct {
	GatewayKey string            `yaml:"gateway_key"`
	APIKeys    map[string]string `yaml:"api_keys"` // provider名 → 実APIキー
}

// Load はencryptedPathをidentityPathの秘密鍵で復号してSecretsを返す。
func Load(encryptedPath, identityPath string) (*Secrets, error) {
	idf, err := os.Open(identityPath)
	if err != nil {
		return nil, fmt.Errorf("age秘密鍵を開けません(%s): %w", identityPath, err)
	}
	defer idf.Close()
	identities, err := age.ParseIdentities(idf)
	if err != nil {
		return nil, fmt.Errorf("age秘密鍵の解析に失敗: %w", err)
	}

	ef, err := os.Open(encryptedPath)
	if err != nil {
		return nil, fmt.Errorf("暗号化secretsを開けません(%s): %w", encryptedPath, err)
	}
	defer ef.Close()
	plain, err := age.Decrypt(ef, identities...)
	if err != nil {
		return nil, fmt.Errorf("age復号に失敗(秘密鍵が一致しない可能性): %w", err)
	}

	data, err := io.ReadAll(plain)
	if err != nil {
		return nil, fmt.Errorf("復号データの読み取りに失敗: %w", err)
	}

	var s Secrets
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("復号後YAMLの解析に失敗: %w", err)
	}
	return &s, nil
}
