// /healthz ハンドラのテスト。
package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/providers"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

func TestHealthPayload(t *testing.T) {
	rt := &Runtime{
		Config: &config.Config{
			DefaultModel: "pi-ollama",
			Models: map[string]config.ModelConfig{
				"pi-ollama":     {Provider: "ollamacloud", Model: "gpt-oss:120b"},
				"pi-openrouter": {Provider: "openrouter", Model: "meta-llama/llama-3.3-70b-instruct"},
			},
			Providers: map[string]config.ProviderConfig{
				"openrouter":  {BaseURL: "https://openrouter.ai/api/v1"},
				"ollamacloud": {BaseURL: "https://ollama.com/v1"},
			},
		},
		Registry:    providers.NewRegistry(),
		Secrets:     &secrets.Secrets{Version: 1, GatewayKey: "k", GatewayKeys: []string{"k2"}, APIKeys: map[string]string{"ollamacloud": "sk"}},
		GatewayKeys: []string{"k"},
	}
	h := New(rt, slog.New(slogDiscardHandler()), nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	h.Health(rec, req)

	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var got healthPayload
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q", got.Status)
	}
	if got.DefaultModel != "pi-ollama" {
		t.Errorf("default_model = %q", got.DefaultModel)
	}
	if len(got.Models) != 2 {
		t.Errorf("models = %d", len(got.Models))
	}
	if m := got.Models["pi-ollama"]; m.Provider != "ollamacloud" || m.Model != "gpt-oss:120b" {
		t.Errorf("models[pi-ollama] = %+v", m)
	}
	if len(got.Providers) != 2 || got.Providers[0] != "ollamacloud" {
		t.Errorf("providers = %v (ソートされていること)", got.Providers)
	}
	if got.GatewayKeys != 2 {
		t.Errorf("gateway_keys = %d", got.GatewayKeys)
	}
}

// slogDiscardHandler はテスト用のログ抑制。
func slogDiscardHandler() slog.Handler {
	return newDiscardHandler()
}
