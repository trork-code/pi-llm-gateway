package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trork-code/pi-llm-gateway/internal/apierr"
	"github.com/trork-code/pi-llm-gateway/internal/audit"
	"github.com/trork-code/pi-llm-gateway/internal/handlers"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// ドーマントモード(design-distribution.md §8 / docs/design-batch3.md §3)。
// 常駐serveは鍵を復号せずに待ち受け(COLD)、認証付きリクエストの到達で warm(復号+Runtime構築)、
// アイドル時間の経過で Secrets.Zero() して COLD へ戻す。/healthz は warm トリガ外(常時応答)。
//
// 状態は handlers.Handlers の Runtime スナップショットで表現する:
//   - COLD: Runtime=nil(GatewayKeysは空→認証付き経路はwarmを先に通るためHTTPには現れない)
//   - WARM: Runtime≠nil(通常運用)
// Warm/Zeroはgate内のmutexで直列化(singleflight: 同時多発リクエストは待ち合流)。
// Zeroはswap後に旧Runtimeのin-flight完了を待ってから Secrets.Zero() する(§8)。

// warmBudget はcoldからのwarm(復号)に許される上限(§8: 設定項目にしない)。
const warmBudget = 10 * time.Second

// dormantGate はcold/warmの状態を管理する。
type dormantGate struct {
	h     *handlers.Handlers
	build func() (*handlers.Runtime, error)
	log   *slog.Logger
	audit *audit.Chain

	idle time.Duration // IDLE_ZERO_MINUTES。0=Zero化しない(常時warm)

	mu       sync.Mutex   // Warm/Zeroの直列化
	inflight atomic.Int64 // 認証済みリクエストの進行数(RuntimeのZero競合防止)
	lastDone atomic.Int64 // 最後の認証済みリクエスト完了時刻(unixnano)

	cold    atomic.Bool
	stopped chan struct{}
	once    sync.Once
}

func newDormantGate(h *handlers.Handlers, build func() (*handlers.Runtime, error), log *slog.Logger, chain *audit.Chain, idle time.Duration) *dormantGate {
	g := &dormantGate{h: h, build: build, log: log, audit: chain, idle: idle, stopped: make(chan struct{})}
	g.cold.Store(true)
	return g
}

// stop ループを止める(テスト/シャットダウン用)。
func (g *dormantGate) stop() { g.once.Do(func() { close(g.stopped) }) }

// Warm はCOLDなら鍵を復号してRuntimeを構築しswapする(WARM)。直列化のため同時呼び出しは待ち合流する。
// budget(10s)超過はfail-closed(ctxを待たず独自タイムアウト: piのHTTPタイムアウトより十分短く抑える)。
func (g *dormantGate) Warm() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.cold.Load() {
		return nil
	}
	start := time.Now()
	type buildResult struct {
		rt  *handlers.Runtime
		err error
	}
	ch := make(chan buildResult, 1)
	go func() {
		rt, err := g.build()
		ch <- buildResult{rt, err}
	}()
	var rt *handlers.Runtime
	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("coldからのwarm(鍵復号)に失敗しました: %w", r.err)
		}
		rt = r.rt
	case <-time.After(warmBudget):
		return fmt.Errorf("warmが%s以内に完了しませんでした(fail-closed)", warmBudget)
	}
	g.h.SwapRuntime(rt)
	g.cold.Store(false)
	g.lastDone.Store(time.Now().UnixNano())
	dur := time.Since(start).Milliseconds()
	if g.log != nil {
		g.log.Info("dormant_warm", "duration_ms", dur)
	}
	if g.audit != nil {
		_ = g.audit.Next(map[string]string{"route": "dormant_warm", "duration_ms": fmt.Sprint(dur)})
	}
	return nil
}

// middleware は認証グループの先頭に差すwarmゲート。coldからの復号を1回に集約し、
// in-flight を数えて idle-zero の競合を防ぐ。
func (g *dormantGate) middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := g.Warm(); err != nil {
				apierr.Write(w, 500, err.Error(), "internal_error", nil)
				return
			}
			g.inflight.Add(1)
			defer func() {
				if v := g.inflight.Add(-1); v == 0 {
					g.lastDone.Store(time.Now().UnixNano())
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// StartIdleZero はアイドル監視ループを起動する(idle<=0なら何もしない)。
func (g *dormantGate) StartIdleZero(tick time.Duration) {
	if g.idle <= 0 {
		return
	}
	if tick <= 0 {
		tick = time.Second
	}
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-g.stopped:
				return
			case <-t.C:
			}
			if g.cold.Load() || g.lastDone.Load() == 0 {
				continue
			}
			if time.Since(time.Unix(0, g.lastDone.Load())) < g.idle {
				continue
			}
			g.Zero()
		}
	}()
}

// Zero はRuntimeをCOLDへ戻し、in-flight完了後に旧Secretsをゼロ化する。
func (g *dormantGate) Zero() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cold.Load() {
		return
	}
	old := g.h.Current()
	g.h.SwapRuntime(nil) // 以後のリクエストはmu待ち→Warmで新Runtime(既存リクエストは旧参照)
	g.cold.Store(true)
	if g.log != nil {
		g.log.Info("dormant_zero", "idle_seconds", int(g.idle.Seconds()))
	}
	if g.audit != nil {
		_ = g.audit.Next(map[string]string{"route": "dormant_zero"})
	}
	// in-flight完了を待ってから鍵を消す(§8: Zeroはin-flight完了後)。
	// 完了待ちはgoroutineに任せる(新規リクエストはcoldのwarm側に流れるため旧Runtimeのin-flightは減るのみ)。
	go func(old *handlers.Runtime) {
		deadline := time.Now().Add(3 * time.Second)
		for g.inflight.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if g.inflight.Load() != 0 {
			if g.log != nil {
				g.log.Warn("dormant_zero: in-flightが完了せず鍵のゼロ化を断念しました(3s)")
			}
			return
		}
		if old != nil && old.Secrets != nil {
			old.Secrets.Zero()
		}
	}(old)
}

// serveDormantOverride はserveフラグ(--dormant/--no-dormant)による上書き(design-batch3.md §3注記)。
// 0=上書きなし / 1=--dormant(強制dormant) / -1=--no-dormant(強制通常ロード)。フラグが最優先。
var serveDormantOverride int8

func dormantFromConfig(state *setup.State, log *slog.Logger) bool {
	switch serveDormantOverride {
	case 1:
		return true
	case -1:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GATEWAY_DORMANT"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return state != nil && strings.EqualFold(state.Startup, "resident")
}

// resolveIdleZero はIDLE_ZERO_MINUTES(env)を解釈する。既定30分、0=無効。
// テスト/検収のため10進小数(例: 0.001=0.06秒)を許可する。
func resolveIdleZero(log *slog.Logger) time.Duration {
	v := strings.TrimSpace(os.Getenv("IDLE_ZERO_MINUTES"))
	if v == "" {
		return 30 * time.Minute
	}
	var mins float64
	if _, err := fmt.Sscanf(v, "%g", &mins); err != nil || mins < 0 {
		if log != nil {
			log.Warn("IDLE_ZERO_MINUTESの解釈に失敗したため既定(30)を使います", "value", v)
		}
		return 30 * time.Minute
	}
	return time.Duration(mins * float64(time.Minute))
}

// identityNeedsPassphrase はpassphrase保護されたidentityを簡易検知する。
// §8: passphrase付きidentity(対話入力が必要)はドーマント不適合=通常ロードへフォールバックする。
// 判定は file/env 由来の中身が素のage identity(AGE-SECRET-KEY-)として読めるかのみ。
// CMD由来は実行がwarm時のため判定不可=適合扱い。
func identityNeedsPassphrase(ident identitySource) bool {
	switch ident.kind {
	case "file":
		b, err := os.ReadFile(ident.file)
		if err != nil {
			return false // 読めない場合は後段(warm時)のエラーに任せる
		}
		return !strings.Contains(string(b), "AGE-SECRET-KEY-") // armor化identityも誤検知される(安全側の不適合)
	case "env":
		return !strings.Contains(ident.inline, "AGE-SECRET-KEY-")
	default:
		return false // CMD由来はwarm時に実行されるため判定不可(適合扱い)
	}
}
