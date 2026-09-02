package providers

import (
	"context"
	"encoding/json"
	"net/http"
)

// AnthropicAdapter はAnthropic Messages API用アダプター。
//
// TODO(ロードマップstep4): ここにOpenAI → Anthropic(/v1/messages)の
// リクエスト変換と、応答(chunk含む)のOpenAI形式への逆変換を実装する。
// 実装後はconfig/models.yamlのprovider差し替えだけで切り替わる。
type AnthropicAdapter struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewAnthropic(baseURL, apiKey string) *AnthropicAdapter {
	return &AnthropicAdapter{BaseURL: baseURL, APIKey: apiKey, HTTP: &http.Client{}}
}

func (a *AnthropicAdapter) Name() string { return ProviderAnthropic }

func (a *AnthropicAdapter) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	return nil, notImplemented()
}

func (a *AnthropicAdapter) ChatStream(ctx context.Context, req *ChatRequest) (<-chan []byte, error) {
	return nil, notImplemented()
}

func notImplemented() *HTTPError {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": "anthropic adapter is not implemented yet (roadmap step 4)",
			"type":    "invalid_request_error",
			"code":    "not_implemented",
		},
	})
	return &HTTPError{Status: http.StatusNotImplemented, Body: body}
}
