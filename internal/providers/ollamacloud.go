package providers

// OllamaCloudAdapter はOllama Cloud (https://ollama.com) 用アダプター。
// OllamaはOpenAI互換API(/v1)を提供しているため、OpenAI素通しadapterをそのまま流用する。
// 実APIキーは ollama.com のAPIキー(secretsの api_keys.ollamacloud)。
// base_urlは https://ollama.com/v1 を想定。
type OllamaCloudAdapter struct {
	*OpenAIAdapter
}

var _ Adapter = (*OllamaCloudAdapter)(nil)

// NewOllamaCloud はollamacloud向けアダプターを作る。
func NewOllamaCloud(baseURL, apiKey string) *OllamaCloudAdapter {
	return &OllamaCloudAdapter{OpenAIAdapter: NewOpenAICompat(ProviderOllamaCloud, baseURL, apiKey)}
}

// Name は埋め込み先の名前を ollamacloud で上書きする。
func (a *OllamaCloudAdapter) Name() string { return ProviderOllamaCloud }
