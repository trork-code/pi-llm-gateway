package providers

// NVIDIAAdapter はNVIDIA NIM (https://build.nvidia.com) 用アダプター。
// NVIDIAのintegrate.api.nvidia.comはOpenAI互換API(/v1)を提供しているため、
// OpenAI素通しadapterをそのまま流用する。
// 実APIキーは NVIDIA APIキー(secretsの api_keys.nvidia)。
// base_urlは https://integrate.api.nvidia.com/v1 を想定。
// modelは "vendor/model名" 形式(例: meta/llama-3.1-405b-instruct)。
type NVIDIAAdapter struct {
	*OpenAIAdapter
}

var _ Adapter = (*NVIDIAAdapter)(nil)

// NewNVIDIA はnvidia向けアダプターを作る。
func NewNVIDIA(baseURL, apiKey string) *NVIDIAAdapter {
	return &NVIDIAAdapter{OpenAIAdapter: NewOpenAICompat(ProviderNVIDIA, baseURL, apiKey)}
}

// Name は埋め込み先の名前を nvidia で上書きする。
func (a *NVIDIAAdapter) Name() string { return ProviderNVIDIA }
