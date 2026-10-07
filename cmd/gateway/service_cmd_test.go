// gateway up | status | down のテスト。
// スポーン経路(実プロセス起動)以外はhttptestで検証する。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

func TestResolveGatewayURLOverride(t *testing.T) {
	if got := resolveGatewayURL("http://localhost:9999"); got != "http://localhost:9999" {
		t.Errorf("override = %q", got)
	}
}

func TestResolveGatewayURLEnvs(t *testing.T) {
	t.Setenv("GATEWAY_ADDR", "http://127.0.0.1:12345")
	t.Setenv("PORT", "")
	if got := resolveGatewayURL(""); got != "http://127.0.0.1:12345" {
		t.Errorf("GATEWAY_ADDR = %q", got)
	}
	t.Setenv("GATEWAY_ADDR", "")
	t.Setenv("GATEWAY_RELOAD_ADDR", "http://127.0.0.1:12346") // 後方互換
	if got := resolveGatewayURL(""); got != "http://127.0.0.1:12346" {
		t.Errorf("GATEWAY_RELOAD_ADDR = %q", got)
	}
	t.Setenv("GATEWAY_RELOAD_ADDR", "")
	t.Setenv("PORT", "18081")
	if got := resolveGatewayURL(""); got != "http://127.0.0.1:18081" {
		t.Errorf("PORT由来 = %q", got)
	}
}

func TestExtractServiceFlags(t *testing.T) {
	sf, err := extractServiceFlags([]string{"-addr", "http://127.0.0.1:2", "-key=abc", "-log", "x.log"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if sf.addr != "http://127.0.0.1:2" || sf.key != "abc" || sf.log != "x.log" {
		t.Errorf("sf = %+v", sf)
	}
	if _, err := extractServiceFlags([]string{"unexpected"}); err == nil {
		t.Error("未知の位置引数が受理された")
	}
}

// serviceCmd のヘルプ表示は正常終了。
func TestServiceCmdHelp(t *testing.T) {
	if err := serviceCmd(opUp, []string{"-h"}); err != nil {
		t.Errorf("help: %v", err)
	}
}

func fakeGateway(t *testing.T, wantKey string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","uptime_seconds":7,"pid":1234,"default_model":"pi-ollama","models":{"pi-ollama":{"provider":"ollamacloud","model":"gpt-oss:120b"}},"providers":["ollamacloud"],"gateway_keys":2,"identity_recipients":1}`))
	})
	mux.HandleFunc("/admin/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if wantKey != "" && r.Header.Get("Authorization") != "Bearer "+wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		_, _ = w.Write([]byte(`{"status":"shutting_down"}`))
	})
	return httptest.NewServer(mux)
}

// status/upが、稼働中のゲートウェイを壊さないことを検証。
func TestServiceStatusAndUpOnRunning(t *testing.T) {
	srv := fakeGateway(t, "")
	defer srv.Close()
	var out strings.Builder
	if err := serviceStatus(discardLog(t), srv.URL, &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "稼働中") || !strings.Contains(out.String(), "pi-ollama") {
		t.Errorf("status出力: %s", out.String())
	}
	out.Reset()
	if err := serviceUp(discardLog(t), srv.URL, "", &out); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out.String(), "稼働中") {
		t.Errorf("up出力: %s", out.String())
	}
}

// downが-gatewayキーをAuthorizationに載せることを確認。
func TestServiceDownSendsKey(t *testing.T) {
	srv := fakeGateway(t, "gk-testkey")
	defer srv.Close()
	var out strings.Builder
	if err := serviceDown(discardLog(t), srv.URL, "gk-testkey", &out); err != nil {
		t.Fatalf("down: %v", err)
	}
	if !strings.Contains(out.String(), "停止を受け付けました") {
		t.Errorf("down出力: %s", out.String())
	}
	if err := serviceDown(discardLog(t), srv.URL, "wrong-key", &strings.Builder{}); err == nil {
		t.Error("誤ったキーでのdownが成功してしまった")
	}
}

// downは未起動ならエラーにしない。
func TestServiceDownWhenNotRunning(t *testing.T) {
	var out strings.Builder
	if err := serviceDown(discardLog(t), "http://127.0.0.1:1", "", &out); err != nil {
		t.Fatalf("down(未起動): %v", err)
	}
	if !strings.Contains(out.String(), "未起動") {
		t.Errorf("down出力: %s", out.String())
	}
}

// health応答のJSONをパースできる(構造一致)。
func TestFetchHealthParses(t *testing.T) {
	srv := fakeGateway(t, "")
	defer srv.Close()
	hz, err := fetchHealth(srv.URL)
	if err != nil {
		t.Fatalf("fetchHealth: %v", err)
	}
	if hz.Status != "ok" || hz.Models["pi-ollama"].Model != "gpt-oss:120b" || len(hz.Providers) != 1 {
		b, _ := json.Marshal(hz)
		t.Errorf("パース結果: %s", b)
	}
}

// resolveGatewayKeyは、-key > env GATEWAY_KEY の順に解決する(secrets経路はidentity envの結合テストでカバー)。
func TestResolveGatewayKeyArgsAndEnv(t *testing.T) {
	if k, err := resolveGatewayKey(discardLog(t), "k-arg"); err != nil || k != "k-arg" {
		t.Errorf("arg = %q err=%v", k, err)
	}
	t.Setenv("GATEWAY_KEY", "k-env")
	if k, err := resolveGatewayKey(discardLog(t), ""); err != nil || k != "k-env" {
		t.Errorf("env = %q err=%v", k, err)
	}
}

// TestResolveGatewayKeyFromSecrets はidentity envでsecretsの1本目を使う。
func TestResolveGatewayKeyFromSecrets(t *testing.T) {
	keysTestEnv(t, &secrets.Secrets{Version: 1, GatewayKey: "gk-base-0001"}) // identity/secrets環境
	t.Setenv("GATEWAY_KEY", "")
	k, err := resolveGatewayKey(discardLog(t), "")
	if err != nil {
		t.Fatalf("secrets経路: %v", err)
	}
	if k != "gk-base-0001" {
		t.Errorf("key = %q, want gk-base-0001", k)
	}
}

// keysTestEnvSet はキューのキーテスト環境を再利用する(このパッケージのテスト用)。
