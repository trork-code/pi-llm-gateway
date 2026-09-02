package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOpenAICompat_Name(t *testing.T) {
	a := NewOpenAICompat("ollamacloud", "https://example.com/v1", "key")
	if a.Name() != "ollamacloud" {
		t.Fatalf("Name() = %q, want ollamacloud", a.Name())
	}
	reg := NewRegistry()
	reg.Register(a)
	if _, ok := reg.Get("ollamacloud"); !ok {
		t.Fatal("registry に ollamacloud が登録されていません")
	}
	if _, ok := reg.Get("openai"); ok {
		t.Fatal("openai は登録されていないはずです")
	}
}

func TestOllamaCloudAdapter_Registered(t *testing.T) {
	a := NewOllamaCloud("https://ollama.com/v1", "key")
	if a.Name() != ProviderOllamaCloud {
		t.Fatalf("Name() = %q, want %q", a.Name(), ProviderOllamaCloud)
	}
	reg := NewRegistry()
	reg.Register(a)
	got, ok := reg.Get(ProviderOllamaCloud)
	if !ok {
		t.Fatal("registry に ollamacloud が登録されていません")
	}
	if got.Name() != ProviderOllamaCloud {
		t.Fatalf("registry から取り出したadapterの名前 = %q", got.Name())
	}
}

func TestOpenAIAdapter_RewriteBody(t *testing.T) {
	a := NewOpenAI("https://example.com/v1", "k")
	out, err := a.rewriteBody(&ChatRequest{
		ResolvedModel: "gpt-oss:120b",
		RawBody:       []byte(`{"model":"pi-ollama","messages":[]}`),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "gpt-oss:120b" || m["stream"] != true {
		t.Fatalf("m = %v", m)
	}
}

func TestOpenAIAdapter_ChatPassthrough(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m, _ := body["model"].(string)
		gotModel = m
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"1","object":"chat.completion"}`)
	}))
	defer srv.Close()

	a := NewOpenAI(srv.URL, "test-key")
	resp, err := a.Chat(context.Background(), &ChatRequest{
		Alias:         "pi-fast",
		ResolvedModel: "gpt-4.1-mini",
		RawBody:       []byte(`{"model":"pi-fast","stream":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotModel != "gpt-4.1-mini" {
		t.Fatalf("model = %q", gotModel)
	}
	if !strings.Contains(string(resp.Body), "chat.completion") {
		t.Fatalf("body = %q", resp.Body)
	}
}

func TestOpenAIAdapter_ChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"2\"}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	a := NewOpenAI(srv.URL, "test-key")
	ch, err := a.ChatStream(context.Background(), &ChatRequest{
		Alias:         "pi-fast",
		ResolvedModel: "gpt-4.1-mini",
		RawBody:       []byte(`{"model":"pi-fast","stream":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for chunk := range ch {
		got = append(got, string(chunk))
	}
	if len(got) != 2 {
		t.Fatalf("chunks = %v", got)
	}
	if !strings.Contains(got[0], "\"id\":\"1\"") {
		t.Fatalf("chunk0 = %q", got[0])
	}
}
