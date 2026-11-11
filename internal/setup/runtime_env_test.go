package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeStdLayout は setup.json 無しの標準配置(manual構成相当)を作る。
func makeStdLayout(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, "config", "models.yaml"),
		filepath.Join(dir, "secrets", "secrets.yaml.age"),
		filepath.Join(dir, "secrets", "identity.txt"),
	} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeLauncherWith(t *testing.T, dir, port, bind string) string {
	t.Helper()
	paths := Layout(dir)
	name := "up.sh"
	if runtime.GOOS == "windows" {
		name = "up.cmd"
	}
	p := filepath.Join(paths.Dir, name)
	// LauncherEnv は cmd 書式(set PORT=)と unix 書式(PORT=)の両方を受理するため、
	// ここでは OS 非依存に行頭 PORT= を書く(unixの export PORT BIND は後段の行)。
	content := "PORT=" + port + "\nBIND=" + bind + "\nexport PORT BIND\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscover_StdLayoutWithoutSetupJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt")
	makeStdLayout(t, dir)
	writeLauncherWith(t, dir, "18081", "127.0.0.1")
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)

	d, err := Discover("")
	if err != nil {
		t.Fatal(err)
	}
	if d.Dir != dir {
		t.Fatalf("dir: %s", d.Dir)
	}
	if d.Config == "" || d.Secrets == "" || d.Identity == "" {
		t.Fatalf("標準配置の発見漏れ: %+v", d)
	}
	if d.Log == "" {
		t.Fatalf("ログ既定が空: %+v", d)
	}
	if d.State != nil {
		t.Fatalf("setup.json が無いのに State: %+v", d.State)
	}
	if d.Port != "18081" || d.Bind != "127.0.0.1" {
		t.Fatalf("ランチャからの port/bind 抽出に失敗: port=%q bind=%q", d.Port, d.Bind)
	}
	if d.NotFound {
		t.Fatal("配置があるのに NotFound")
	}
}

func TestDiscover_SetupJSONTakesPriorityOverLauncher(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt")
	makeStdLayout(t, dir)
	writeLauncherWith(t, dir, "18081", "127.0.0.1")
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	paths := Layout(dir)
	if err := SaveState(paths, &State{Port: 18099, Bind: "127.0.0.2", IdentityMode: "file"}); err != nil {
		t.Fatal(err)
	}

	d, err := Discover("")
	if err != nil {
		t.Fatal(err)
	}
	if d.State == nil {
		t.Fatal("setup.json が読めていない")
	}
	if d.Port != "18099" {
		t.Fatalf("setup.json がランチャより優先されるべき: port=%q", d.Port)
	}
	if d.Bind != "127.0.0.2" {
		t.Fatalf("setup.json の bind が採用されるべき: bind=%q", d.Bind)
	}
}

func TestDiscover_EmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "empty")
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)

	d, err := Discover("")
	if err != nil {
		t.Fatal(err)
	}
	if !d.NotFound {
		t.Fatalf("空 dir は NotFound: %+v", d)
	}
	if d.Config != "" || d.Secrets != "" || d.Identity != "" || d.Port != "" || d.Bind != "" {
		t.Fatalf("空 dir で発見値がある: %+v", d)
	}
}

func TestLauncherEnv_ParsesLauncherFormats(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt")
	paths := Layout(dir)
	if err := os.MkdirAll(paths.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var content string
	if runtime.GOOS == "windows" {
		content = "set PORT=18080\r\nset BIND=127.0.0.1\r\n"
	} else {
		content = "PORT=18080\nBIND=127.0.0.1\nexport PORT BIND\n"
	}
	if err := os.WriteFile(paths.Launcher, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	port, bind := LauncherEnv(paths)
	if port != "18080" || bind != "127.0.0.1" {
		t.Fatalf("抽出に失敗: port=%q bind=%q", port, bind)
	}
}
