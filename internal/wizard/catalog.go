// Package wizard は `gateway init`(配布ウィザード)を実装する。
// 対話フロー・非対話モード・管理メニューの3形態が同じ runSetup コアを共有する。
//
// 設計: docs/design-distribution.md §2 (対話フロー完全版) / §3 (providerカタログ)
package wizard

import (
	"fmt"
	"sort"
)

// Provider はカタログ1件(ウィザード既定値の source of truth)。
type Provider struct {
	ID        string
	Title     string
	BaseURL   string
	EnvVar    string // 環境変数検知の対象
	Suggest   string // 推奨モデル(設問[3/5]の既定値)
	Note      string // プロンプト直下に1行で出す注意
	CW        int    // pi側既定 contextWindow
	Input     []string
	Reasoning bool
	Disabled  bool // anthropic 等、未実装のため選択肢に出さない
}

// Catalog は対応プロバイダー一覧(§3 表)。順序は提示順で固定。
func Catalog() []Provider {
	return []Provider{
		{ID: "ollamacloud", Title: "Ollama Cloud (ollama.com)", BaseURL: "https://ollama.com/v1",
			EnvVar: "OLLAMA_API_KEY", Suggest: "gpt-oss:120b",
			Note: "ollama.com models require a tag (e.g. gpt-oss:120b); bare names fail upstream.",
			CW:   131072, Input: []string{"text"}, Reasoning: true},
		{ID: "openai", Title: "OpenAI", BaseURL: "https://api.openai.com/v1",
			EnvVar: "OPENAI_API_KEY", Suggest: "gpt-4.1-mini",
			CW: 128000, Input: []string{"text", "image"}, Reasoning: true},
		{ID: "openrouter", Title: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1",
			EnvVar: "OPENROUTER_API_KEY", Suggest: "meta-llama/llama-3.3-70b-instruct",
			Note: "model is `vendor/model` form.",
			CW:   131072, Input: []string{"text"}, Reasoning: true},
		{ID: "groq", Title: "Groq", BaseURL: "https://api.groq.com/openai/v1",
			EnvVar: "GROQ_API_KEY", Suggest: "llama-3.3-70b-versatile",
			CW: 131072, Input: []string{"text"}, Reasoning: true},
		{ID: "nvidia", Title: "NVIDIA NIM", BaseURL: "https://integrate.api.nvidia.com/v1",
			EnvVar: "NVIDIA_API_KEY", Suggest: "meta/llama-3.1-405b-instruct",
			Note: "model is `vendor/model` form.",
			CW:   131072, Input: []string{"text"}, Reasoning: true},
		{ID: "anthropic", Title: "Anthropic", BaseURL: "https://api.anthropic.com/v1",
			EnvVar: "ANTHROPIC_API_KEY", Suggest: "",
			Disabled: true}, // 未実装(501スタブ) §3
	}
}

// Look は catalog から id 引き。
func Look(id string) (Provider, bool) {
	for _, p := range Catalog() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// EnabledIDs は選択可能な provider id 一覧(テスト・doctor用)。
func EnabledIDs() []string {
	ids := make([]string, 0, 8)
	for _, p := range Catalog() {
		if !p.Disabled {
			ids = append(ids, p.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// DetectEnv は環境変数走査で最初に見つかった provider を返す(提案用)。
// 複数検知時は Catalog の提示順優先。
func DetectEnv(getenv func(string) string) (Provider, bool) {
	for _, p := range Catalog() {
		if p.Disabled {
			continue
		}
		if getenv(p.EnvVar) != "" {
			return p, true
		}
	}
	return Provider{}, false
}

// fp は鍵の先頭4文字(表示用)。
func fp(key string) string {
	if len(key) <= 4 {
		return "…"
	}
	return fmt.Sprintf("%s…", key[:4])
}
