package providers

// GroqAdapter はGroq (https://groq.com) 用アダプター。
// GroqはOpenAI互換API(https://api.groq.com/openai/v1)を提供しているため、
// OpenAI素通しadapterをそのまま流用する。
// 実APIキーは console.groq.com のAPIキー(secretsの api_keys.groq)。
type GroqAdapter struct {
	*OpenAIAdapter
}

var _ Adapter = (*GroqAdapter)(nil)

// NewGroq はgroq向けアダプターを作る。
func NewGroq(baseURL, apiKey string) *GroqAdapter {
	return &GroqAdapter{OpenAIAdapter: NewOpenAICompat(ProviderGroq, baseURL, apiKey)}
}

// Name は埋め込み先の名前を groq で上書きする。
func (a *GroqAdapter) Name() string { return ProviderGroq }
