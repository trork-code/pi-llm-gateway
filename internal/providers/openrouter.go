package providers

// OpenRouterAdapter はOpenRouter (https://openrouter.ai) 用アダプター。
// OpenRouterはOpenAI互換API(/v1)を提供しているため、OpenAI素通しadapterをそのまま流用する。
// 実APIキーは openrouter.ai のAPIキー(secretsの api_keys.openrouter)。
// base_urlは https://openrouter.ai/api/v1 を想定。
// modelは "vendor/model名" 形式(例: meta-llama/llama-3.3-70b-instruct)。
type OpenRouterAdapter struct {
	*OpenAIAdapter
}

var _ Adapter = (*OpenRouterAdapter)(nil)

// NewOpenRouter はopenrouter向けアダプターを作る。
func NewOpenRouter(baseURL, apiKey string) *OpenRouterAdapter {
	return &OpenRouterAdapter{OpenAIAdapter: NewOpenAICompat(ProviderOpenRouter, baseURL, apiKey)}
}

// Name は埋め込み先の名前を openrouter で上書きする。
func (a *OpenRouterAdapter) Name() string { return ProviderOpenRouter }
