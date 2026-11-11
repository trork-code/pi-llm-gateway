// gateway up | status | down のテスト。
// スポーン経路(実プロセス起動)以外はhttptestで検証する。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"gopkg.in/yaml.v3"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
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

// TestResolveGatewayPort_FromRuntimeDir は PORT env 無しのとき
// runtime dir(setup.json > ランチャ解析)から接続先ポートを補う(runtime dir 統合)。
func TestResolveGatewayPort_FromRuntimeDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt")
	paths := setup.Layout(dir)
	if err := os.MkdirAll(paths.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := setup.SaveState(paths, &setup.State{Port: 18099, Bind: "127.0.0.1", IdentityMode: "file"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	for _, k := range []string{"PORT", "GATEWAY_ADDR", "GATEWAY_RELOAD_ADDR"} {
		_ = os.Unsetenv(k)
	}
	if got := resolveGatewayPort(); got != "18099" {
		t.Fatalf("setup.json のポートが採用されるべき: %q", got)
	}
	if got := resolveGatewayURL(""); got != "http://127.0.0.1:18099" {
		t.Fatalf("接続先URL: %q", got)
	}
}

// TestLoadSecretsForCLI_RuntimeDirDiscovery は env 無しでも runtime dir の
// secrets/identity.txt を発見して復号できる(runtime dir 統合)。
func TestLoadSecretsForCLI_RuntimeDirDiscovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt")
	paths := setup.Layout(dir)
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Identity), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte("default_model: pi-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Identity, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestSecretsTo(t, paths.Secrets, id, &secrets.Secrets{Version: 1, GatewayKey: "gk-disc-0001"})

	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	for _, k := range []string{"AGE_IDENTITY_FILE", "AGE_IDENTITY", "AGE_IDENTITY_CMD",
		"AGE_PASSPHRASE", "SECRETS_DECRYPT_CMD", "SECRETS_FILE"} {
		_ = os.Unsetenv(k)
	}

	sec, err := loadSecretsForCLI(discardLog(t))
	if err != nil {
		t.Fatalf("runtime dir 発見で復号できるべき: %v", err)
	}
	defer sec.Zero()
	keys := sec.AllGatewayKeys()
	if len(keys) != 1 || keys[0] != "gk-disc-0001" {
		t.Fatalf("gateway キー不一致: %v", keys)
	}
}

// keysTestEnvSet はキューのキーテスト環境を再利用する(このパッケージのテスト用)。

// writeTestSecretsTo はsecを指定パスへage暗号化(armor)して書き出す。
// (keysTestEnvのwriteTestSecretsはファイル名固定のため、runtime layout用に別途用意)
func writeTestSecretsTo(t *testing.T, path string, id *age.X25519Identity, sec *secrets.Secrets) {
	t.Helper()
	plain, err := yaml.Marshal(sec)
	if err != nil {
		t.Fatalf("YAMLの生成: %v", err)
	}
	var buf strings.Builder
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, id.Recipient())
	if err != nil {
		t.Fatalf("age暗号化: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("暗号化書き込み: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("age close: %v", err)
	}
	if err := aw.Close(); err != nil {
		t.Fatalf("armor close: %v", err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("secretsの書き出し(%s): %v", path, err)
	}
}
