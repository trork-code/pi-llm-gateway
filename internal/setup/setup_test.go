package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadState_Missing(t *testing.T) {
	dir := t.TempDir()
	paths := Layout(dir)
	st, err := LoadState(paths)
	if err != nil || st != nil {
		t.Fatalf("未セットアップでは(nil, nil)を期待: st=%v err=%v", st, err)
	}
	if IsComplete(paths, st) {
		t.Fatal("config+secrets無しは未セットアップ")
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths := Layout(dir)
	in := &State{Port: 18080, Bind: "127.0.0.1", Startup: "manual", IdentityMode: "file",
		Providers: []string{"ollamacloud"}, Aliases: []string{"pi-default"}}
	if err := SaveState(paths, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if out.Port != 18080 || out.Version != 1 || out.CreatedAt == "" || out.UpdatedAt == "" {
		t.Fatalf("roundtrip不一致: %+v", out)
	}
	// 2回目の保存で created_at が保持されるか
	created := out.CreatedAt
	out.Port = 18081
	if err := SaveState(paths, out); err != nil {
		t.Fatal(err)
	}
	again, err := LoadState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if again.CreatedAt != created {
		t.Fatalf("created_atが更新されてしまった: %s != %s", again.CreatedAt, created)
	}
	if again.Port != 18081 {
		t.Fatalf("port保持失敗: %d", again.Port)
	}
}

func TestLoadState_RejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	paths := Layout(dir)
	if err := AtomicBytes(paths.SetupJSON, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(paths); err == nil {
		t.Fatal("未対応versionは拒否されるべき")
	}
}

func TestIsComplete_LegacyManualEnv(t *testing.T) {
	dir := t.TempDir()
	paths := Layout(dir)
	// setup.json 無しでも config+secrets が揃っていれば完了扱い(既存手動環境互換)
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicBytes(paths.Config, []byte("default_model: x\nmodels: {x: {provider: p, model: m}}\nproviders: {p: {base_url: 'https://x/v1'}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicBytes(paths.Secrets, []byte("---"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsComplete(paths, nil) {
		t.Fatal("手動構築環境(config+secrets有)はセットアップ済みとみなすべき")
	}
}

func TestSelfCopy(t *testing.T) {
	dir := t.TempDir()
	paths := Layout(dir)
	src := filepath.Join(t.TempDir(), "gateway-src")
	if err := os.WriteFile(src, []byte("fake-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// os.Executable() は差し替えられないため、CopyFile経由の内容検証を行う
	if err := CopyFile(src, paths.Bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// SelfCopy の「ロック時温存」はos.Executable依存のため Windows実機検証(local-init-e2e)でカバー
	got, err := os.ReadFile(paths.Bin)
	if err != nil || string(got) != "fake-binary" {
		t.Fatalf("コピー不正: err=%v content=%q", err, got)
	}
}

func TestGuard(t *testing.T) {
	t.Setenv("GATEWAY_RUNTIME_DIR", filepath.Join(t.TempDir(), "pi-gateway"))
	if !Guard() {
		t.Fatal("未セットアップ+env指定無しはガードが効くべき")
	}
	t.Setenv("CONFIG_FILE", "x.yaml")
	if Guard() {
		t.Fatal("env明示(CI・既存デプロイ)はガードを無視すべき")
	}
}

func TestLayoutPaths(t *testing.T) {
	dir := "C:/x/pi-gateway"
	paths := Layout(dir)
	if runtime.GOOS == "windows" {
		if filepath.Base(paths.Bin) != "gateway.exe" || !strings.HasSuffix(paths.Launcher, "up.cmd") {
			t.Fatalf("windows配置: %s %s", paths.Bin, paths.Launcher)
		}
	} else {
		if filepath.Base(paths.Bin) != "gateway" || !strings.HasSuffix(paths.Launcher, "up.sh") {
			t.Fatalf("unix配置: %s %s", paths.Bin, paths.Launcher)
		}
	}
}

func TestAtomicWrite_ReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := AtomicBytes(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicBytes(path, []byte("two!"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "two!" {
		t.Fatalf("差し替え失敗: %q err=%v", got, err)
	}
	tmpLeft, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".f.txt.tmp-*"))
	if len(tmpLeft) != 0 {
		t.Fatalf("一時ファイルが残った: %v", tmpLeft)
	}
}

func TestLauncherWrite(t *testing.T) {
	dir := t.TempDir()
	paths := Layout(dir)
	err := WriteLauncher(paths, LauncherInput{Port: "18080", Bind: "127.0.0.1", Log: paths.Log, IdentityMode: "file"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.Launcher)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if runtime.GOOS == "windows" {
		if !strings.Contains(s, "AGE_IDENTITY_FILE=") || !strings.Contains(s, "gateway.exe\" up") {
			t.Fatalf("windowsランチャ不正: %s", s)
		}
	} else {
		if !strings.Contains(s, "export AGE_IDENTITY_FILE") {
			t.Fatalf("unixランチャ不正: %s", s)
		}
	}
	// env: / cmd: モードでも鍵値を書かないこと
	if err := WriteLauncher(paths, LauncherInput{Port: "18080", Log: paths.Log, IdentityMode: "env", IdentityValue: "MY_KEY_ENV"}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(paths.Launcher)
	if !strings.Contains(string(data), "MY_KEY_ENV") || strings.Contains(string(data), "AGE_IDENTITY_FILE=") {
		t.Fatalf("env:モードのランチャ不正: %s", data)
	}
	if runtime.GOOS == "windows" && !strings.Contains(string(data), "set AGE_IDENTITY=%MY_KEY_ENV%") {
		t.Fatalf("windows env:モード不正: %s", data)
	}
}
