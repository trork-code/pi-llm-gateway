package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/providers"
)

type stubAdapter struct{}

func (stubAdapter) Name() string { return providers.ProviderOpenAI }

func (stubAdapter) Chat(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{
		Status: http.StatusOK,
		Body:   []byte(`{"id":"chatcmpl-1","object":"chat.completion"}`),
	}, nil
}

func (stubAdapter) ChatStream(ctx context.Context, req *providers.ChatRequest) (<-chan []byte, error) {
	ch := make(chan []byte, 1)
	ch <- []byte(`{"id":"x","choices":[{"delta":{"content":"hi"}}]}`)
	close(ch)
	return ch, nil
}

func newTestHandlers(t *testing.T) *Handlers {
	t.Helper()
	reg := providers.NewRegistry()
	reg.Register(stubAdapter{})
	return New(newTestRuntime(t, "pi-fast", "gpt-4.1-mini"), slog.Default(), nil)
}

func newTestRuntime(t *testing.T, alias, model string) *Runtime {
	t.Helper()
	cfg := &config.Config{
		DefaultModel: alias,
		Models: map[string]config.ModelConfig{
			alias: {Provider: "openai", Model: model},
		},
		Providers: map[string]config.ProviderConfig{
			"openai": {BaseURL: "http://localhost:1"},
		},
	}
	reg := providers.NewRegistry()
	reg.Register(stubAdapter{})
	return &Runtime{Config: cfg, Registry: reg, GatewayKeys: []string{"k"}}
}

func TestSwapRuntime(t *testing.T) {
	h := New(newTestRuntime(t, "pi-fast", "gpt-4.1-mini"), slog.Default(), nil)

	// 差し替え前は旧エイリアスで応答
	if ids := modelIDs(t, h); len(ids) != 1 || ids[0] != "pi-fast" {
		t.Fatalf("ids = %v", ids)
	}

	// 差し替え後は新エイリアスで応答する
	h.SwapRuntime(newTestRuntime(t, "pi-slow", "other-model"))
	if ids := modelIDs(t, h); len(ids) != 1 || ids[0] != "pi-slow" {
		t.Fatalf("after swap: ids = %v", ids)
	}
}

func modelIDs(t *testing.T, h *Handlers) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	h.Models(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got.Data))
	for _, m := range got.Data {
		id, _ := m["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func TestModels(t *testing.T) {
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	h.Models(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0]["id"] != "pi-fast" {
		t.Fatalf("data = %v", got.Data)
	}
}

func TestChatCompletions_Passthrough(t *testing.T) {
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"pi-fast","stream":false,"messages":[]}`))
	h.ChatCompletions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "chat.completion") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestChatCompletions_Stream(t *testing.T) {
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"pi-fast","stream":true,"messages":[]}`))
	h.ChatCompletions(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestChatCompletions_UnknownModel(t *testing.T) {
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"nope"}`))
	h.ChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestChatCompletions_MissingModel(t *testing.T) {
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	h.ChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRequestLogger(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	RequestLogger(log, nil)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
}
