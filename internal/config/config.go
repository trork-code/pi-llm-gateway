// Package config はconfig/models.yamlの読み込みと検証を行う。
// ここに秘密は一切書かない(実キーはsecretsパッケージが管理する)。
package config

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// ProviderConfig は上流プロバイダーの接続情報(非秘匿)。
type ProviderConfig struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"` // 将来のフォールバック用。実キーはsecretsから渡す
}

// ModelConfig はエイリアス1つ分の対応。
type ModelConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// Config はmodels.yaml全体。
type Config struct {
	DefaultModel string                    `yaml:"default_model"`
	Models       map[string]ModelConfig    `yaml:"models"`
	Providers    map[string]ProviderConfig `yaml:"providers"`
}

// Load はpathのYAMLを読み込み、検証して返す。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%sを読めません: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%sのYAML解析に失敗: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%sの検証に失敗: %w", path, err)
	}
	return &c, nil
}

// Validate は存在しないproviderを参照するエイリアスなどを起動時に弾く。
// 新規約(pi id=実モデル名)では model: フィールドは省略可(エントリキー=実モデル名と扱う)。
// 省略時はここでキー値への補完を行い、以降の処理(ResolveRequest/handler)は
// どちらの記法でも同一に動く。
func (c *Config) Validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("models が空です")
	}
	if _, ok := c.Models[c.DefaultModel]; !ok {
		return fmt.Errorf("default_model %q が models に存在しません", c.DefaultModel)
	}
	for key, mc := range c.Models {
		if mc.Model == "" {
			mc.Model = key // 実モデル名キー: model省略はキーと同義(新規約)
			c.Models[key] = mc
		}
		if mc.Provider == "" || mc.Model == "" {
			return fmt.Errorf("model %q: provider/model は必須です", key)
		}
		pc, ok := c.Providers[mc.Provider]
		if !ok {
			return fmt.Errorf("model %q が存在しないprovider %q を参照しています", key, mc.Provider)
		}
		if pc.BaseURL == "" {
			return fmt.Errorf("provider %q: base_url は必須です", mc.Provider)
		}
	}
	return nil
}

// Resolve はエイリアスから(実provider名, 実モデル名)を引く。
func (c *Config) Resolve(alias string) (ModelConfig, bool) {
	mc, ok := c.Models[alias]
	return mc, ok
}

// ResolveRequest はリクエストのmodel idを解決する。
// 規約(2026-11新設): pi側のidは実モデル名そのもの(minimax-m3.1-flash 等)。
//
//	(1) 先にエイリアスキーの完全一致(旧規約・既存設定との互換)
//	(2) 無ければ各エントリの model フィールドが一致するもの(=実モデル名キー)
//
// 同一実モデルが複数エントリにある場合(例: 別プロバイダーで同名モデル)は
// 辞書順最初のキーを採用する(設定上、providerは一意に決めておく運用を推奨。
// doctorが重複を警告する)。
func (c *Config) ResolveRequest(id string) (ModelConfig, bool) {
	if mc, ok := c.Resolve(id); ok {
		return mc, true
	}
	var keys []string
	for k, mc := range c.Models {
		if mc.Model == id {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ModelConfig{}, false
	}
	sort.Strings(keys)
	return c.Models[keys[0]], true
}

// ModelLegacy はエントリがエイリアスキー(キー != 実モデル名)かを返す。
func (c *Config) ModelLegacy(key string, mc ModelConfig) bool {
	return key != mc.Model
}

// ModelNameSet は全エントリの実モデル名(重複なし・ソート済み)を返す。
// pi側が送ってくるidの期待集合(doctorの照合に使う)。
func (c *Config) ModelNameSet() []string {
	set := map[string]bool{}
	for _, mc := range c.Models {
		set[mc.Model] = true
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// LegacyAliases は旧形式エントリ(キー != 実モデル名)のキー一覧(ソート済み)。
func (c *Config) LegacyAliases() []string {
	var out []string
	for k, mc := range c.Models {
		if k != mc.Model {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// FindOwnersOfModel は指定実モデル名を指すエントリ(キー一覧)を返す。
func (c *Config) FindOwnersOfModel(name string) []string {
	var keys []string
	for k, mc := range c.Models {
		if k == name || mc.Model == name {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
