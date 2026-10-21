package wizard

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

func TestResolveAPIKeySource(t *testing.T) {
	if k, err := ResolveAPIKeySource("plain-key", nil); err != nil || k != "plain-key" {
		t.Fatalf("plain: %v %v", k, err)
	}
	t.Setenv("WIZ_TEST_KEY", "from-env")
	if k, err := ResolveAPIKeySource("env:WIZ_TEST_KEY", nil); err != nil || k != "from-env" {
		t.Fatalf("env: %v %v", k, err)
	}
	if _, err := ResolveAPIKeySource("env:WIZ_TEST_MISSING", nil); err == nil {
		t.Fatal("未設定envはエラーになるべき")
	}
	f := filepath.Join(t.TempDir(), "k.txt")
	os.WriteFile(f, []byte(" from-file \n"), 0o600)
	if k, err := ResolveAPIKeySource("file:"+f, nil); err != nil || k != "from-file" {
		t.Fatalf("file: %v %v", k, err)
	}
	if k, err := ResolveAPIKeySource("-", strings.NewReader("  from-stdin \n\n")); err != nil || k != "from-stdin" {
		t.Fatalf("stdin: %v %v", k, err)
	}
	if _, err := ResolveAPIKeySource("", nil); err == nil {
		t.Fatal("空はエラー")
	}
}

func TestGenerateGatewayKey(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		k, err := GenerateGatewayKey()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(k, "gk-") || len(k) != 3+32 {
			t.Fatalf("形式不正: %q", k)
		}
		if seen[k] {
			t.Fatalf("重複生成: %q", k)
		}
		seen[k] = true
	}
}

func TestCatalog(t *testing.T) {
	ids := EnabledIDs()
	if len(ids) != 5 {
		t.Fatalf("選択可能providerは5つ: %v", ids)
	}
	for _, id := range ids {
		p, ok := Look(id)
		if !ok || p.BaseURL == "" || p.EnvVar == "" || p.Suggest == "" || p.CW == 0 {
			t.Fatalf("カタログ不完全: %s -> %+v", id, p)
		}
	}
	if a, ok := Look("anthropic"); ok && !a.Disabled {
		t.Fatal("anthropicは未実装のためDisabledであるべき")
	}
	p, found := DetectEnv(func(name string) string {
		if name == "GROQ_API_KEY" {
			return "x"
		}
		return ""
	})
	if !found || p.ID != "groq" {
		t.Fatalf("env検知失敗: %v %v", found, p)
	}
}

// runSetupOnce は非対話initを1回実行し、pi models.json をテスト場所へ向ける。
func runSetupOnce(t *testing.T, dir, alias string) *Summary {
	t.Helper()
	t.Setenv("GATEWAY_PI_MODELS", filepath.Join(dir, "pi", "agent", "models.json"))
	sum, err := RunSetup(Options{
		RuntimeDir: dir,
		Provider:   "ollamacloud",
		APIKeySpec: "env:WIZ_FLOW_KEY",
		Model:      "gpt-oss:120b",
		Alias:      alias,
		Pi:         true,
		Startup:    "manual",
		Port:       "18099",
		Yes:        true,
		Quiet:      true, // 上流検証(実ネットワーク)はテスト対象外
		Stdin:      strings.NewReader(""),
	}, io.Discard)
	if err != nil {
		t.Fatalf("RunSetup失敗: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv("GATEWAY_PI_MODELS") })
	return sum
}

func TestRunSetup_FullFlow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WIZ_FLOW_KEY", "test-provider-key-123")
	sum := runSetupOnce(t, dir, "pi-e2e")

	// runtime dir の構成物
	paths := setup.Layout(dir)
	for _, p := range []string{paths.SetupJSON, paths.Config, paths.Identity, paths.Secrets, paths.Launcher, paths.Bin} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("生成物が無い: %s (%v)", p, err)
		}
	}
	// identity は owner-only(WindowsのPerm()は常に0666表示のためunixのみ検査)
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(paths.Identity); err == nil {
			if st.Mode().Perm()&0o077 != 0 {
				t.Fatalf("identity の権限が緩い: %v", st.Mode())
			}
		}
	}
	// config は config.Load で検証可能(起動可能形)
	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatalf("生成configが不正: %v", err)
	}
	if cfg.DefaultModel != "pi-e2e" {
		t.Fatalf("default_model不正: %s", cfg.DefaultModel)
	}
	if _, ok := cfg.Resolve("pi-e2e"); !ok {
		t.Fatal("エイリアスが無い")
	}

	// secrets は identity で復号でき、鍵が入っている
	idData, _ := os.ReadFile(paths.Identity)
	sec, err := secrets.Load(secrets.LoadOptions{EncryptedPath: paths.Secrets,
		Identity: strings.NewReader(strings.TrimSpace(string(idData)))})
	if err != nil {
		t.Fatal(err)
	}
	if keys := sec.AllGatewayKeys(); len(keys) != 1 || !strings.HasPrefix(keys[0], "gk-") {
		t.Fatalf("gatewayキー不正: %v", keys)
	}
	if sec.APIKeys["ollamacloud"] != "test-provider-key-123" {
		t.Fatalf("providerキー不正: %v", sec.APIKeys)
	}
	if !strings.HasPrefix(sum.GatewayKeyFP, "gk-") {
		t.Fatalf("サマリのFP不正: %q", sum.GatewayKeyFP)
	}

	// pi models.json: gatewayのみ書かれている
	piPath := os.Getenv("GATEWAY_PI_MODELS")
	data, _ := os.ReadFile(piPath)
	var top map[string]any
	json.Unmarshal(data, &top)
	prov := top["providers"].(map[string]any)
	gw, ok := prov["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("gatewayブロックが無い: %s", data)
	}
	if gw["apiKey"].(string) != sec.AllGatewayKeys()[0] {
		t.Fatalf("models.jsonのキーがsecretsと不一致: %v vs %v", gw["apiKey"], sec.AllGatewayKeys()[0])
	}
	if gw["baseUrl"] != "http://127.0.0.1:18099/v1" {
		t.Fatalf("baseUrl不正: %v", gw["baseUrl"])
	}
	if gw["api"] != "openai-completions" {
		t.Fatalf("api不正: %v", gw["api"])
	}
	// setup.json にキー値が載っていない
	stData, _ := os.ReadFile(paths.SetupJSON)
	if strings.Contains(string(stData), "test-provider-key-123") || strings.Contains(string(stData), "gk-") {
		t.Fatalf("setup.jsonに秘匿値が混入: %s", stData)
	}
	// ランチャに identity 値を書かない(file modeでidentityパス参照のみ)
	launcher, _ := os.ReadFile(paths.Launcher)
	if strings.Contains(string(launcher), "test-provider-key-123") ||
		strings.Contains(string(launcher), sec.AllGatewayKeys()[0]) {
		t.Fatal("ランチャに実キーが書かれた")
	}
}

func TestRunSetup_RerunPreservesGatewayKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WIZ_FLOW_KEY", "test-provider-key-123")
	runSetupOnce(t, dir, "pi-e2e")
	piPath := os.Getenv("GATEWAY_PI_MODELS")

	keyOf := func() string {
		data, _ := os.ReadFile(piPath)
		var top map[string]any
		json.Unmarshal(data, &top)
		gw := top["providers"].(map[string]any)["gateway"].(map[string]any)
		return gw["apiKey"].(string)
	}
	before := keyOf()
	// 同じ内容で2回目: キーは流用され、ブロックが重複しない
	runSetupOnce(t, dir, "pi-e2e")
	if got := keyOf(); got != before {
		t.Fatalf("gatewayキーが流用されなかった: %s != %s", got, before)
	}
	data, _ := os.ReadFile(piPath)
	if strings.Count(string(data), `"apiKey"`) != 1 {
		t.Fatalf("gatewayブロックが重複: %s", data)
	}
	// 別エイリアス追加(メニュー相当)後に3回目 → models.json に反映
	paths := setup.Layout(dir)
	secData, _ := os.ReadFile(paths.Config)
	if err := ops.UpsertAlias(paths.Config, ops.AliasSpec{Alias: "pi-second",
		Provider: "ollamacloud", Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	_ = secData
	runSetupOnce(t, dir, "pi-e2e")
	data, _ = os.ReadFile(piPath)
	if !strings.Contains(string(data), "pi-second") || !strings.Contains(string(data), "pi-e2e") {
		t.Fatalf("models.jsonに新aliasが反映されていない: %s", data)
	}
	if keyOf() != before {
		t.Fatal("キーが変更された")
	}
	// backupが作られている(2回目以降)
	baks, _ := filepath.Glob(piPath + ".bak-*")
	if len(baks) == 0 {
		t.Fatal("2回目以降の実行でバックアップが作られるべき")
	}
}

func TestRunSetup_ResidentDowngradesToManual(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WIZ_FLOW_KEY", "k")
	out := &strings.Builder{}
	if _, err := RunSetup(Options{RuntimeDir: dir, Provider: "ollamacloud",
		APIKeySpec: "env:WIZ_FLOW_KEY", Startup: "resident", Port: "18099",
		Yes: true, Stdin: strings.NewReader("")}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "later release") {
		t.Fatalf("案内が無い: %s", out)
	}
}

func TestRunSetup_Errors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WIZ_FLOW_KEY", "k")
	t.Setenv("GATEWAY_PI_MODELS", filepath.Join(dir, "pi", "agent", "models.json"))
	if _, err := RunSetup(Options{Provider: "anthropic", APIKeySpec: "env:WIZ_FLOW_KEY",
		Yes: true, Stdin: strings.NewReader("")}, io.Discard); err == nil {
		t.Fatal("anthropic(未実装)は拒否されるべき")
	}
	if _, err := RunSetup(Options{Provider: "ollamacloud", APIKeySpec: "env:WIZ_NOPE",
		Yes: true, Stdin: strings.NewReader("")}, io.Discard); err == nil {
		t.Fatal("未設定envはエラーになるべき")
	}
	if _, err := RunSetup(Options{Provider: "ollamacloud", APIKeySpec: "env:WIZ_FLOW_KEY",
		Alias: "bad alias!", Yes: true, Stdin: strings.NewReader("")}, io.Discard); err == nil {
		t.Fatal("不正aliasは拒否されるべき")
	}
	if _, err := RunSetup(Options{Provider: "ollamacloud", Yes: true,
		Stdin: strings.NewReader("")}, io.Discard); err == nil {
		t.Fatal("api-key未指定はエラーになるべき")
	}
	// openai は正当(providerとして有効)。runtime必須(実環境へ触れない)
	if _, err := RunSetup(Options{RuntimeDir: dir, Provider: "openai", Model: "gpt-4.1-mini",
		APIKeySpec: "env:WIZ_FLOW_KEY", Yes: true, Quiet: true,
		Stdin: strings.NewReader("")}, io.Discard); err != nil {
		t.Fatalf("openaiは有効であるべき: %v", err)
	}
}
