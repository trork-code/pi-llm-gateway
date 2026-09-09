package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// restoreAllowlist はグローバルなegress設定をテスト後に戻す。
func restoreAllowlist(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = SetEgressAllowlist(nil)
	})
}

func TestEgressGuard_BlocksUnlistedHost(t *testing.T) {
	_ = SetEgressAllowlist([]string{"api.example.com"})
	defer restoreAllowlist(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	a := NewOpenAI(srv.URL, "test-key")
	_, err := a.Chat(context.Background(), &ChatRequest{
		Alias:         "x",
		ResolvedModel: "m",
		RawBody:       []byte(`{"model":"x"}`),
	})
	if err == nil {
		t.Fatal("許可リスト外のホストへの接続が拒否されていません")
	}
}

func TestEgressGuard_AllowsListedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"1","object":"chat.completion"}`)
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	_ = SetEgressAllowlist([]string{host})
	defer restoreAllowlist(t)

	a := NewOpenAI(srv.URL, "test-key")
	resp, err := a.Chat(context.Background(), &ChatRequest{
		Alias:         "x",
		ResolvedModel: "m",
		RawBody:       []byte(`{"model":"x"}`),
	})
	if err != nil {
		t.Fatalf("許可済みホストへの接続に失敗: %v", err)
	}
	if len(resp.Body) == 0 {
		t.Fatal("応答が空です")
	}
}

func TestEgressGuard_Disabled(t *testing.T) {
	defer restoreAllowlist(t)
	_ = SetEgressAllowlist(nil) // 無効化

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"1"}`)
	}))
	defer srv.Close()

	a := NewOpenAI(srv.URL, "test-key")
	if _, err := a.Chat(context.Background(), &ChatRequest{
		Alias:         "x",
		ResolvedModel: "m",
		RawBody:       []byte(`{"model":"x"}`),
	}); err != nil {
		t.Fatalf("egress制御無効時は接続できるべき: %v", err)
	}
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}

func TestSetEgressAllowlist_EmptyHost(t *testing.T) {
	if err := SetEgressAllowlist([]string{" "}); err == nil {
		t.Fatal("空ホストは拒否されるべき")
	}
	// 失敗しても以前の状態が壊れていないこと(空解除のみ有効)
	if err := SetEgressAllowlist(nil); err != nil {
		t.Fatalf("nilで無効化できない: %v", err)
	}
}
