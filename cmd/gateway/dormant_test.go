package main

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trork-code/pi-llm-gateway/internal/audit"
	"github.com/trork-code/pi-llm-gateway/internal/handlers"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// testGate はテスト用のdormantGateを組む(buildはテストのRuntimeを返す)。
func testGate(t *testing.T, build func() (*handlers.Runtime, error), idle time.Duration) (*dormantGate, *handlers.Handlers) {
	t.Helper()
	chain, err := audit.NewChain()
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.New(nil, discardLog(t), chain) // COLD起動(nil Runtime許可)
	g := newDormantGate(h, build, discardLog(t), chain, idle)
	if !g.cold.Load() {
		t.Fatal("初期状態はCOLDであるべき")
	}
	return g, h
}

func testRuntime(t *testing.T) *handlers.Runtime {
	t.Helper()
	sec := &secrets.Secrets{Version: 1, GatewayKey: "gk-dormant-0001"}
	return &handlers.Runtime{Secrets: sec, GatewayKeys: sec.AllGatewayKeys()}
}

// TestDormantGate_WarmZeroLoop は COLD→Warm(復号)→Zero(idle経過でZero回帰)の一巡を検証する。
func TestDormantGate_WarmZeroLoop(t *testing.T) {
	builds := atomic.Int64{}
	g, _ := testGate(t, func() (*handlers.Runtime, error) {
		builds.Add(1)
		return testRuntime(t), nil
	}, 0) // idle無効(ループは使わずZeroを直接呼ぶ)

	if err := g.Warm(); err != nil {
		t.Fatalf("Warm失敗: %v", err)
	}
	if builds.Load() != 1 {
		t.Fatalf("build呼び出し回数: %d", builds.Load())
	}
	if g.cold.Load() {
		t.Fatal("Warm後はWARMであるべき")
	}
	cur := g.h.Current()
	if cur == nil || cur.Secrets.AllGatewayKeys()[0] != "gk-dormant-0001" {
		t.Fatalf("Warm後のRuntime不正: %v", cur)
	}

	g.Zero()
	if !g.cold.Load() {
		t.Fatal("Zero後はCOLDであるべき")
	}
	time.Sleep(50 * time.Millisecond) // in-flight待ちgoroutineの完了を待つ
	if k := cur.Secrets.AllGatewayKeys(); len(k) != 0 {
		t.Fatalf("Zero後に鍵が残っている: %v", k)
	}
}

// TestDormantGate_IdleZeroAuto はアイドル経過で自動的にCOLDへ戻ることを検証する。
func TestDormantGate_IdleZeroAuto(t *testing.T) {
	g, _ := testGate(t, func() (*handlers.Runtime, error) { return testRuntime(t), nil }, 5*time.Millisecond)
	g.StartIdleZero(1 * time.Millisecond)
	defer g.stop()
	if err := g.Warm(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.cold.Load() {
			return // Zero回帰を確認
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("idle経過でCOLDに戻らなかった")
}

// TestDormantGate_WarmFailure は復号失敗がfail-closed(COLD維持+エラー)であることを検証する。
func TestDormantGate_WarmFailure(t *testing.T) {
	calls := atomic.Int64{}
	g, _ := testGate(t, func() (*handlers.Runtime, error) {
		calls.Add(1)
		return nil, os.ErrPermission
	}, 0)
	if err := g.Warm(); err == nil {
		t.Fatal("build失敗はエラーになるべき")
	}
	if !g.cold.Load() {
		t.Fatal("失敗後もCOLDを維持するべき")
	}
	if err := g.Warm(); err == nil {
		t.Fatal("再試行も同じくエラー")
	}
	if calls.Load() != 2 {
		t.Fatalf("build呼び出し回数: %d", calls.Load())
	}
}

// TestDormantGate_Singleflight は同時Warmがbuildを1回に集約すること(singleflight)を検証する。
func TestDormantGate_Singleflight(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	builds := atomic.Int64{}
	g, _ := testGate(t, func() (*handlers.Runtime, error) {
		once.Do(func() { close(started) })
		builds.Add(1)
		time.Sleep(15 * time.Millisecond) // 疑似復号
		return testRuntime(t), nil
	}, 0)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = g.Warm()
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("Warm[%d]エラー: %v", i, e)
		}
	}
	if builds.Load() != 1 {
		t.Fatalf("同時Warmはbuild1回に集約されるべき: %d", builds.Load())
	}
}

// TestDormantFromConfig はserve起動時の判定(env優先、setup.json.startupで既定)を検証する。
func TestDormantFromConfig(t *testing.T) {
	if dormantFromConfig(nil, discardLog(t)) {
		t.Fatal("env無し+setup.json無しはdormant off")
	}
	if dormantFromConfig(&setup.State{Startup: "manual"}, discardLog(t)) {
		t.Fatal("manual構成はdormant off")
	}
	if !dormantFromConfig(&setup.State{Startup: "resident"}, discardLog(t)) {
		t.Fatal("resident構成はdormant on")
	}
	t.Setenv("GATEWAY_DORMANT", "1")
	if !dormantFromConfig(nil, discardLog(t)) {
		t.Fatal("GATEWAY_DORMANT=1はsetup.json無しでもon")
	}
	t.Setenv("GATEWAY_DORMANT", "0")
	if dormantFromConfig(&setup.State{Startup: "resident"}, discardLog(t)) {
		t.Fatal("GATEWAY_DORMANT=0はresidentでもoff")
	}
}

// TestResolveIdleZero は IDLE_ZERO_MINUTES(env)の解釈(既定30分/小数対応/0=無効)を検証する。
func TestResolveIdleZero(t *testing.T) {
	if got := resolveIdleZero(discardLog(t)); got != 30*time.Minute {
		t.Fatalf("既定は30分: %v", got)
	}
	t.Setenv("IDLE_ZERO_MINUTES", "10")
	if got := resolveIdleZero(discardLog(t)); got != 10*time.Minute {
		t.Fatalf("10分: %v", got)
	}
	t.Setenv("IDLE_ZERO_MINUTES", "0.001")
	if got := resolveIdleZero(discardLog(t)); got != 60*time.Millisecond {
		t.Fatalf("小数(0.001分=60ms): %v", got)
	}
	t.Setenv("IDLE_ZERO_MINUTES", "0")
	if got := resolveIdleZero(discardLog(t)); got != 0 {
		t.Fatalf("0=無効: %v", got)
	}
}

// TestIdentityNeedsPassphrase はpassphrase保護identityの簡易検知(§8不適合)を検証する。
func TestIdentityNeedsPassphrase(t *testing.T) {
	dir := t.TempDir()
	plain := dir + "/identity.txt"
	if err := os.WriteFile(plain, []byte("AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if identityNeedsPassphrase(identitySource{kind: "file", file: plain}) {
		t.Fatal("素のidentityはpassphrase無しとみなすべき")
	}
	encd := dir + "/identity.enc"
	if err := os.WriteFile(encd, []byte("-----BEGIN AGE ENCRYPTED FILE-----\nZW5jcnlwdGVk\n-----END AGE ENCRYPTED FILE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !identityNeedsPassphrase(identitySource{kind: "file", file: encd}) {
		t.Fatal("armor化(passphrase想定)identityは不適合と判定すべき")
	}
	if identityNeedsPassphrase(identitySource{kind: "cmd", cmd: "age -d"}) {
		t.Fatal("CMD由来は判定不可=適合扱い")
	}
}
