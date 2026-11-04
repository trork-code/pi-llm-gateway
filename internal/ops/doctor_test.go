package ops

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// doctorEnv はテスト用に runtime dir 一式を組み立てる。
// 実環境 ~/.pi-gateway は決して触らない(t.TempDir + 空きポート動的確保)。
type doctorEnv struct {
	dir      string
	paths    setup.Paths
	identity *age.X25519Identity
	port     string // setup.jsonポート(空きポートから選ぶ)
	piPath   string
	binVer   string // 注入する bin 版
	withBin  bool
}

func newDoctorEnv(t *testing.T) *doctorEnv {
	t.Helper()
	dir := t.TempDir()
	paths := setup.Layout(dir)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("identity生成: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Identity), 0o700); err != nil {
		t.Fatalf("secrets dir作成: %v", err)
	}
	if err := os.WriteFile(paths.Identity, []byte(id.String()), 0o600); err != nil {
		t.Fatalf("identity書込: %v", err)
	}
	return &doctorEnv{dir: dir, paths: paths, identity: id, port: pickFreePort(t)}
}

// pickFreePort は固定ポート衝突を避けるため空きポートを動的に選ぶ。
func pickFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("空きポート取得: %v", err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func (e *doctorEnv) writeSecrets(t *testing.T) string {
	t.Helper()
	key := "gk-doctortest0123456789"
	sec := &secrets.Secrets{
		Version:     1,
		GatewayKeys: []string{key},
		APIKeys:     map[string]string{"ollamacloud": "sk-fake-ollamacloud-key"},
	}
	if err := secrets.Save(e.paths.Secrets, sec, []string{e.identity.Recipient().String()}); err != nil {
		t.Fatalf("secrets保存: %v", err)
	}
	return key
}

func (e *doctorEnv) writeConfig(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.paths.Config), 0o755); err != nil {
		t.Fatalf("config dir作成: %v", err)
	}
	if err := os.WriteFile(e.paths.Config, []byte(
		"default_model: a1\nmodels:\n  a1:\n    provider: ollamacloud\n    model: gpt-oss:120b\nproviders:\n  ollamacloud:\n    base_url: https://ollama.com/v1\n"), 0o644); err != nil {
		t.Fatalf("config書込: %v", err)
	}
}

func (e *doctorEnv) writeSetupJSON(t *testing.T, piRegistered bool) {
	t.Helper()
	port, _ := strconv.Atoi(e.port)
	st := &setup.State{
		Version:      1,
		Port:         port,
		Bind:         "127.0.0.1",
		Startup:      "manual",
		IdentityMode: "file",
		Providers:    []string{"ollamacloud"},
		Aliases:      []string{"a1"},
		DefaultModel: "a1",
		PiRegistered: piRegistered,
	}
	if err := setup.SaveState(e.paths, st); err != nil {
		t.Fatalf("setup.json書込: %v", err)
	}
}

func (e *doctorEnv) writePi(t *testing.T, apiKey string, extraModels ...string) {
	t.Helper()
	models := []map[string]any{{"id": "a1", "_launch": true, "reasoning": false}}
	for _, m := range extraModels {
		models = append(models, map[string]any{"id": m, "_launch": true, "reasoning": false})
	}
	pi := map[string]any{"providers": map[string]any{"gateway": map[string]any{
		"api":     "openai-completions",
		"apiKey":  apiKey,
		"baseUrl": fmt.Sprintf("http://127.0.0.1:%s/v1", e.port),
		"models":  models,
	}}}
	data, _ := json.Marshal(pi)
	e.piPath = filepath.Join(e.dir, "pi-models.json")
	if err := os.WriteFile(e.piPath, data, 0o600); err != nil {
		t.Fatalf("pi models書込: %v", err)
	}
}

func (e *doctorEnv) writeBin(t *testing.T, version string) {
	t.Helper()
	if err := os.MkdirAll(e.paths.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.paths.Bin, []byte("dummy"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.binVer, e.withBin = version, true
	// ランチャ(7. autostartの存在検査対象)
	if err := os.WriteFile(e.paths.Launcher, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// opts はdoctor実行条件を組み立てる。
// Addr は "http://127.0.0.1:<空きでない固定>" ではなく検証確実な cold 判定のため
// 応答しないリスナを使う(呼び出し側で上書き可)。
func (e *doctorEnv) opts(t *testing.T) DoctorOptions {
	t.Helper()
	unreachable := unreachableAddr(t)
	return DoctorOptions{
		Dir:          e.dir,
		Addr:         unreachable,
		CLIVersion:   "0.2.0",
		PiModelsPath: e.piPath,
		MaxAgeDays:   90,
		LoadSecrets: func() (*secrets.Secrets, error) {
			return secrets.Load(secrets.LoadOptions{
				EncryptedPath: e.paths.Secrets,
				Identity:      strings.NewReader(e.identity.String()),
			})
		},
		BinVersion: func(binPath string) (string, error) {
			if !e.withBin {
				return "", fmt.Errorf("no bin")
			}
			return e.binVer, nil
		},
	}
}

// unreachableAddr は即座に接続拒否になるアドレス(cold判定を環境非依存に固定)。
func unreachableAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cold判定用リスナ: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // 閉じておく → 接続は即拒否される
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func (r *DoctorReport) byNo(no int) Check {
	for _, c := range r.Checks {
		if c.No == no {
			return c
		}
	}
	return Check{Status: "missing"}
}

// TestDoctorAllOK は全項目ok(cold含む)でfail=0になること。
func TestDoctorAllOK(t *testing.T) {
	e := newDoctorEnv(t)
	key := e.writeSecrets(t)
	e.writeConfig(t)
	e.writeSetupJSON(t, true)
	e.writePi(t, key)
	e.writeBin(t, "0.2.0")

	rep, err := Doctor(e.opts(t))
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if got := len(rep.Checks); got != 10 {
		t.Fatalf("check数 = %d, want 10", got)
	}
	if rep.Failures() != 0 {
		for _, c := range rep.Checks {
			if c.Status == CheckFail {
				t.Errorf("check %d (%s) failed: %s", c.No, c.Label, c.Detail)
			}
		}
		t.Fatalf("failures = %d, want 0", rep.Failures())
	}
	if c := rep.byNo(6); c.Status != CheckOK || !strings.Contains(c.Detail, "cold") {
		t.Errorf("check6(cold) = %s/%s", c.Status, c.Detail)
	}
	for _, no := range []int{2, 3, 4, 5, 7, 8} {
		if c := rep.byNo(no); c.Status != CheckOK {
			t.Errorf("check%d = %s/%s", no, c.Status, c.Detail)
		}
	}
	if c := rep.byNo(9); c.Status != CheckOK || !strings.Contains(c.Detail, "0.2.0") {
		t.Errorf("check9 = %s/%s", c.Status, c.Detail)
	}
}

// TestDoctorMissingRuntime は空dir → item1 fail・fail>0。
func TestDoctorMissingRuntime(t *testing.T) {
	e := newDoctorEnv(t)
	rep, err := Doctor(e.opts(t))
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if c := rep.byNo(1); c.Status != CheckFail {
		t.Errorf("check1 = %s, want fail (%s)", c.Status, c.Detail)
	}
	if rep.Failures() == 0 {
		t.Error("failures = 0, want > 0")
	}
}

// TestDoctorConfigFailure はconfig壊れ → item2 fail + item5 skip。
func TestDoctorConfigFailure(t *testing.T) {
	e := newDoctorEnv(t)
	e.writeSecrets(t)
	if err := os.MkdirAll(filepath.Dir(e.paths.Config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.paths.Config, []byte(
		"default_model: a1\nmodels:\n  a1:\n    provider: ollamacloud\n    model: m\n"), 0o644); err != nil {
		t.Fatal(err)
	} // providers未定義 → Validate失敗
	e.writeSetupJSON(t, false)
	e.writePi(t, "gk-doctortest0123456789")
	e.writeBin(t, "0.2.0")

	rep, err := Doctor(e.opts(t))
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if c := rep.byNo(2); c.Status != CheckFail {
		t.Errorf("check2 = %s (%s)", c.Status, c.Detail)
	}
	if c := rep.byNo(5); c.Status != CheckSkip {
		t.Errorf("check5 = %s, want skip", c.Status)
	}
}

// TestDoctorPiKeyMismatch はpi側キー不一致 → item4 fail(fix提案付き)。
func TestDoctorPiKeyMismatch(t *testing.T) {
	e := newDoctorEnv(t)
	key := e.writeSecrets(t)
	e.writeConfig(t)
	e.writeSetupJSON(t, true)
	e.writePi(t, "gk-other-key-not-in-secrets")
	e.writeBin(t, "0.2.0")

	rep, err := Doctor(e.opts(t))
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	c := rep.byNo(4)
	if c.Status != CheckFail {
		t.Fatalf("check4 = %s (%s)", c.Status, c.Detail)
	}
	if c.Fix == "" {
		t.Error("check4 fix suggestion missing")
	}
	if key == "" {
		t.Error("gw key empty (test infra broken)")
	}
}

// TestDoctorPiDrift はpi側にstale alias → item5 warn(failではない)。
func TestDoctorPiDrift(t *testing.T) {
	e := newDoctorEnv(t)
	key := e.writeSecrets(t)
	e.writeConfig(t)
	e.writeSetupJSON(t, true)
	e.writePi(t, key, "stale-alias")
	e.writeBin(t, "0.2.0")

	rep, err := Doctor(e.opts(t))
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	c := rep.byNo(5)
	if c.Status != CheckWarn || !strings.Contains(c.Detail, "stale in pi") {
		t.Errorf("check5 = %s/%s", c.Status, c.Detail)
	}
	if rep.Failures() != 0 {
		t.Errorf("drift only → failures = %d, want 0 (warn>0はexit 0)", rep.Failures())
	}
}

// TestDoctorKeyAgeStale は鍵齢超過 → item3 warn・fix提案にrotate。
func TestDoctorKeyAgeStale(t *testing.T) {
	e := newDoctorEnv(t)
	key := e.writeSecrets(t)
	e.writeConfig(t)
	e.writeSetupJSON(t, true)
	e.writePi(t, key)
	e.writeBin(t, "0.2.0")

	stale := time.Now().Add(-120 * 24 * time.Hour)
	if err := os.Chtimes(e.paths.Secrets, stale, stale); err != nil {
		t.Skipf("Chtimes不可環境: %v", err)
	}
	rep, err := Doctor(e.opts(t))
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	c := rep.byNo(3)
	if c.Status != CheckWarn {
		t.Fatalf("check3 = %s (%s)", c.Status, c.Detail)
	}
	if !strings.Contains(c.Fix, "secrets rotate") {
		t.Errorf("check3 fix = %q", c.Fix)
	}
}

// TestDoctorPrintOutput は表示(Stdout)契約: [fail]行とFix節。
func TestDoctorPrintOutput(t *testing.T) {
	e := newDoctorEnv(t)
	var buf strings.Builder
	o := e.opts(t)
	o.Stdout = &buf
	if _, err := Doctor(o); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"pi-llm-gateway doctor", "result:", "Fix:", "[fail]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q not found:\n%s", want, out)
		}
	}
}
