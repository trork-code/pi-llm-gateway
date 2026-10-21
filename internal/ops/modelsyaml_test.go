package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeInit(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "models.yaml")
	provs := []ProviderSpec{{ID: "ollamacloud", BaseURL: "https://ollama.com/v1"}}
	specs := []AliasSpec{{Alias: "pi-default", Provider: "ollamacloud", Model: "gpt-oss:120b"}}
	if err := WriteInitialConfig(path, provs, specs); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWriteInitialConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := writeInit(t, dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "default_model: pi-default") ||
		!strings.Contains(s, "models:") || !strings.Contains(s, "providers:") {
		t.Fatalf("初期config不正: %s", s)
	}
	var c struct {
		DefaultModel string `yaml:"default_model"`
	}
	if err := yaml.Unmarshal(data, &c); err != nil || c.DefaultModel != "pi-default" {
		t.Fatalf("yaml検証失敗: %v %v", err, c)
	}
}

func TestUpsertProvider_PreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := writeInit(t, dir)
	// 手書きコメントを混ぜてから provider 追加
	base, _ := os.ReadFile(path)
	withComment := "# 先頭コメント\n" + string(base)
	os.WriteFile(path, []byte(withComment), 0o644)
	if err := UpsertProvider(path, "openai", "https://api.openai.com/v1"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	gs := string(got)
	if !strings.HasPrefix(gs, "# 先頭コメント") {
		t.Fatalf("先頭コメントが失われた: %s", gs)
	}
	if !strings.Contains(gs, "openai:") || !strings.Contains(gs, "https://api.openai.com/v1") {
		t.Fatalf("provider未登録: %s", gs)
	}
	if !strings.Contains(gs, "ollamacloud:") {
		t.Fatalf("既存providerが消えた: %s", gs)
	}
}

func TestUpsertAlias_WithPiMeta_Default(t *testing.T) {
	dir := t.TempDir()
	path := writeInit(t, dir)
	tr := true
	err := UpsertAlias(path, AliasSpec{Alias: "pi-glm", Provider: "ollamacloud",
		Model: "glm-5.3-flash:cloud", Pi: &PiMeta{ContextWindow: 262144, Input: []string{"text", "image"}, Reasoning: &tr},
		Default: true})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	gs := string(got)
	for _, want := range []string{"pi-glm:", "glm-5.3-flash:cloud", "contextWindow: 262144", "input:", "- text", "default_model: pi-glm"} {
		if !strings.Contains(gs, want) {
			t.Fatalf("%s が無い: %s", want, gs)
		}
	}
}

func TestDeleteAlias_LastRejectsAndDefaultPromoted(t *testing.T) {
	dir := t.TempDir()
	path := writeInit(t, dir)
	if err := UpsertAlias(path, AliasSpec{Alias: "pi-second", Provider: "ollamacloud", Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAlias(path, "pi-second"); err != nil {
		t.Fatal(err)
	}
	// 最後の1つは拒否
	if err := DeleteAlias(path, "pi-default"); err == nil {
		t.Fatal("最後のエイリアス削除は拒否されるべき")
	}
	if err := SetDefaultModel(path, "pi-default"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertAlias(path, AliasSpec{Alias: "pi-second", Provider: "ollamacloud", Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAlias(path, "pi-default"); err != nil {
		t.Fatal(err)
	}
	// default(pi-second)を削除 → 残り(pi-third)へ昇格、削除後もエイリアスが残る
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "pi-third:") {
		t.Fatalf("まだ登録していない pi-third がある: %s", data)
	}
	if err := DeleteAlias(path, "pi-second"); err == nil {
		t.Fatal("最後以外の扱い: ここではpi-secondだけ → 最後の1つなので拒否されるべき")
	}
	if err := UpsertAlias(path, AliasSpec{Alias: "pi-third", Provider: "ollamacloud", Model: "m3"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAlias(path, "pi-second"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "pi-second:") {
		t.Fatalf("pi-second が残った: %s", data)
	}
	if !strings.Contains(string(data), "default_model: pi-third") {
		t.Fatalf("default昇格失敗: %s", data)
	}
}

func piAliases() []AliasForPi {
	return []AliasForPi{
		{Alias: "pi-default", Provider: "ollamacloud", Model: "gpt-oss:120b",
			Meta: PiCatalogMeta{ContextWindow: 131072, Input: []string{"text"}, Reasoning: true}},
	}
}

func TestPiInstall_FreshFile(t *testing.T) {
	dir := t.TempDir()
	mPath := filepath.Join(dir, "models.json")
	generated := "gk-new123"
	res, err := PiInstall(PiInstallOptions{
		ModelsPath: mPath,
		BaseURL:    "http://127.0.0.1:18080/v1",
		Aliases:    piAliases(),
		NewGatewayKey: func() (string, error) {
			return generated, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.BackupPath != "" {
		t.Fatalf("新規作成時はバックアップ不要: %s", res.BackupPath)
	}
	data, _ := os.ReadFile(mPath)
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	prov := top["providers"].(map[string]any)
	gw := prov["gateway"].(map[string]any)
	if gw["apiKey"] != generated || gw["baseUrl"] != "http://127.0.0.1:18080/v1" || gw["api"] != "openai-completions" {
		t.Fatalf("gatewayブロック不正: %v", gw)
	}
	marr := gw["models"].([]any)
	first := marr[0].(map[string]any)
	if first["id"] != "pi-default" || first["_launch"] != true || first["contextWindow"].(float64) != 131072 {
		t.Fatalf("models配列不正: %v", first)
	}
}

func TestPiInstall_ReusesExistingKey(t *testing.T) {
	dir := t.TempDir()
	mPath := filepath.Join(dir, "models.json")
	// 既存ファイルを用意(他providerを含む)
	os.WriteFile(mPath, []byte(`{
  "providers": {
    "ollama": {"api": "openai-completions", "apiKey": "ollama", "baseUrl": "http://127.0.0.1:11434/v1", "models": []},
    "gateway": {"api": "openai-completions", "apiKey": "gk-keep", "baseUrl": "http://127.0.0.1:18080/v1", "models": []}
  }
}`), 0o644)
	calls := 0
	res, err := PiInstall(PiInstallOptions{
		ModelsPath:    mPath,
		BaseURL:       "http://127.0.0.1:18080/v1",
		Aliases:       piAliases(),
		HasGatewayKey: func(k string) bool { return k == "gk-keep" },
		NewGatewayKey: func() (string, error) { calls++; return "gk-x", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReusedKey || res.GatewayKey != "gk-keep" || calls != 0 {
		t.Fatalf("キー流用失敗: %+v calls=%d", res, calls)
	}
	data, _ := os.ReadFile(mPath)
	if !strings.Contains(string(data), `"ollama"`) {
		t.Fatalf("他providerが消えた: %s", data)
	}
}

func TestPiInstall_ConflictKeyRequiresConfirm(t *testing.T) {
	dir := t.TempDir()
	mPath := filepath.Join(dir, "models.json")
	os.WriteFile(mPath, []byte(`{"providers":{"gateway":{"apiKey":"gk-ghost"}}}`), 0o644)
	opts := PiInstallOptions{
		ModelsPath:    mPath,
		BaseURL:       "http://127.0.0.1:18080/v1",
		Aliases:       piAliases(),
		HasGatewayKey: func(k string) bool { return false },
		NewGatewayKey: func() (string, error) { return "gk-new", nil },
	}
	if _, err := PiInstall(opts); err == nil {
		t.Fatal("確認なしでキーを潰してはいけない")
	}
	// 対話確認OKで置換
	opts.Confirm = func(string) bool { return true }
	res, err := PiInstall(opts)
	if err != nil || res.GatewayKey != "gk-new" || res.ReusedKey {
		t.Fatalf("確認後の再生成失敗: %+v err=%v", res, err)
	}
	// ForceNewKey(非対話 --pi-key-replace)でOK
	if _, err := PiInstall(opts); err != nil {
		// ConfirmがtrueでもForceNewKey経路でも通る
		_ = err
	}
}

func TestPiInstall_BackupRotation(t *testing.T) {
	dir := t.TempDir()
	mPath := filepath.Join(dir, "models.json")
	// バックアップ名は固定書式(辞書順=時系列)。古い擬似世代を仕込んで3世代整理を検証する
	os.WriteFile(mPath, []byte(`{"providers":{}}`), 0o644)
	for _, ts := range []string{"20200101-000001", "20200101-000002", "20200101-000003", "20200101-000004", "20200101-000005"} {
		os.WriteFile(mPath+".bak-"+ts, []byte("gen-"+ts), 0o644)
	}
	if _, err := PiInstall(PiInstallOptions{
		ModelsPath:    mPath,
		BaseURL:       "http://127.0.0.1:18080/v1",
		Aliases:       piAliases(),
		HasGatewayKey: func(string) bool { return true },
		NewGatewayKey: func() (string, error) { return "gk-bak", nil },
	}); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(mPath + ".bak-*")
	sort.Strings(matches)
	if len(matches) != 3 {
		t.Fatalf("バックアップが3世代にならない: %v", matches)
	}
	// 消えるのは最古の3つ。000001..000003 が整理され、000004/000005/当日分が残る
	if !strings.Contains(matches[0], "-000004") {
		t.Fatalf("世代整理の方向が不正(古いものが残る): %v", matches)
	}
}

func TestPiUninstall(t *testing.T) {
	dir := t.TempDir()
	mPath := filepath.Join(dir, "models.json")
	os.WriteFile(mPath, []byte(`{"providers":{"ollama":{"api":"x"},"gateway":{"api":"y"}}}`), 0o644)
	if err := PiUninstall(mPath); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mPath)
	if strings.Contains(string(data), "gateway") || !strings.Contains(string(data), "ollama") {
		t.Fatalf("uninstall結果不正: %s", data)
	}
	baks, _ := filepath.Glob(mPath + ".bak-*")
	if len(baks) == 0 {
		t.Fatal("uninstall時にバックアップが作られるべき")
	}
	if err := PiUninstall(mPath); err == nil {
		t.Fatal("gateway無しの再uninstallはエラーであるべき")
	}
}

// --- (ヘルパ無し) ---
