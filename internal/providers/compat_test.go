package providers

import "testing"

// compatProviders はNewOpenAICompat経由で追加したOpenAI互換プロバイダーの一覧。
// 各adapterが正しいprovider名でregistryに登録できることを一括検証する。
func TestCompatProviders_Registered(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		new     func(baseURL, apiKey string) Adapter
		want    string
	}{
		{"ollamacloud", "https://ollama.com/v1", func(b, k string) Adapter { return NewOllamaCloud(b, k) }, ProviderOllamaCloud},
		{"openrouter", "https://openrouter.ai/api/v1", func(b, k string) Adapter { return NewOpenRouter(b, k) }, ProviderOpenRouter},
		{"groq", "https://api.groq.com/openai/v1", func(b, k string) Adapter { return NewGroq(b, k) }, ProviderGroq},
		{"nvidia", "https://integrate.api.nvidia.com/v1", func(b, k string) Adapter { return NewNVIDIA(b, k) }, ProviderNVIDIA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.new(tt.baseURL, "key")
			if a.Name() != tt.want {
				t.Fatalf("Name() = %q, want %q", a.Name(), tt.want)
			}
			reg := NewRegistry()
			reg.Register(a)
			got, ok := reg.Get(tt.want)
			if !ok {
				t.Fatalf("registry に %s が登録されていません", tt.want)
			}
			if got.Name() != tt.want {
				t.Fatalf("registry から取り出したadapterの名前 = %q", got.Name())
			}
		})
	}
}
