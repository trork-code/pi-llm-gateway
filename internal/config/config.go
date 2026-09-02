// Package config はconfig/models.yamlの読み込みと検証を行う。
// ここに秘密は一切書かない(実キーはsecretsパッケージが管理する)。
package config

import (
	"fmt"
	"os"

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
func (c *Config) Validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("models が空です")
	}
	if _, ok := c.Models[c.DefaultModel]; !ok {
		return fmt.Errorf("default_model %q が models に存在しません", c.DefaultModel)
	}
	for alias, mc := range c.Models {
		if mc.Provider == "" || mc.Model == "" {
			return fmt.Errorf("model %q: provider/model は必須です", alias)
		}
		pc, ok := c.Providers[mc.Provider]
		if !ok {
			return fmt.Errorf("model %q が存在しないprovider %q を参照しています", alias, mc.Provider)
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
