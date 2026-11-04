// doctor — セットアップ環境の整合性診断(design-distribution.md §10)。
//
// 「動いているか」(= `gateway status` の薄い表示)ではなく「この環境は壊れていないか、
// 直すならどの窓口を触ればいいか」に答える。未稼働(cold)は正常扱い。
//
// 出力契約:
//   - 検査行は [ok] / [warn] / [fail] / [skip]
//   - 末尾に fix 提案(該当メニュー番号 or フラグ1行)
//   - exit code は fail>0 → 1, warn>0 → 0(呼び出し側 cmd が Failures() で判定)
package ops

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// CheckStatus は検査行の状態。
type CheckStatus string

const (
	CheckOK   CheckStatus = "ok"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
	CheckSkip CheckStatus = "skip"
)

// Check は検査1項目分。
type Check struct {
	No     int // 検査番号(§10の 1〜10)
	Status CheckStatus
	Label  string
	Detail string
	Fix    string // 空なら提案なし
}

// DoctorReport は10項目の診断結果。
type DoctorReport struct {
	Dir    string
	Checks []Check
}

// Count は指定状態の検査数を返す。
func (r *DoctorReport) Count(s CheckStatus) int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == s {
			n++
		}
	}
	return n
}

// Failures は fail の数(doctor の exit code 判定用)。fail>0 → exit 1。
func (r *DoctorReport) Failures() int { return r.Count(CheckFail) }

// Warnings は warn の数。warn>0 でも exit 0(スクリプトから判定可能に)。
func (r *DoctorReport) Warnings() int { return r.Count(CheckWarn) }

// DoctorOptions は doctor の入力。
type DoctorOptions struct {
	Dir string // runtime dir(空なら既定 ~/.pi-gateway を解決)
	// Addr は /healthz の接続先(空なら GATEWAY_ADDR / GATEWAY_RELOAD_ADDR /
	// http://127.0.0.1:<port> の順で決める)
	Addr string
	// CLIVersion は実行中CLIのバージョン(空なら版差比較はせずbin版のみ表示)
	CLIVersion string
	// PiModelsPath は pi 側 models.json(空なら ~/.pi/agent/models.json。
	// テストフック GATEWAY_PI_MODELS も含めて呼び出し側で解決して渡す)
	PiModelsPath string
	// MaxAgeDays は secrets 鍵齢のしきい値(<=0 で鍵齢警告を無効化)
	MaxAgeDays int64
	// LoadSecrets は復号器(環境側で用意)。nil や復号失敗は 3. secrets に反映
	LoadSecrets func() (*secrets.Secrets, error)
	// BinVersion は runtime bin の版取得(省略時は `bin -v` を実行。
	// テストでは注入して実行を回避できる)
	BinVersion func(binPath string) (string, error)
	// Stdout が非 nil ならレポートを同時に表示する
	Stdout io.Writer
}

// Doctor は§10の10項目を検査する。内部エラー(fatal)は第2戻り値、
// 個別検査の失敗は Check(fail) に反映される。
func Doctor(opts DoctorOptions) (*DoctorReport, error) {
	dir := opts.Dir
	if dir == "" {
		d, err := setup.ResolveDir("")
		if err != nil {
			return nil, err
		}
		dir = d
	}
	paths := setup.Layout(dir)
	rep := &DoctorReport{Dir: dir}

	state, stateErr := setup.LoadState(paths)
	rep.add(doctorRuntimeDir(paths, state, stateErr)) // 1
	rep.add(doctorConfig(paths, state))               // 2
	rep.add(doctorSecrets(paths, state, opts))        // 3
	port := doctorPort(paths)                         //   (接続先ポートの解決)
	piPath := opts.PiModelsPath
	if piPath == "" {
		piPath = defaultPiModelsPath()
	}
	rep.check(doctorPi(paths, port, piPath, opts)...) // 4 + 5
	runCheck, warm := doctorRunning(opts, port)       // 6
	rep.add(runCheck)
	rep.add(doctorAutostart(paths, state))            // 7
	rep.add(doctorPortBind(paths, state, port, warm)) // 8
	rep.check(doctorBinary(paths, opts)...)           // 9
	rep.add(doctorCodepage())                         // 10

	if opts.Stdout != nil {
		printDoctor(opts.Stdout, rep)
	}
	return rep, nil
}

func (r *DoctorReport) add(c Check) { r.Checks = append(r.Checks, c) }

// check は複数検査を登録する。
func (r *DoctorReport) check(cs ...Check) { r.Checks = append(r.Checks, cs...) }

// printDoctor はレポートを表示する。
func printDoctor(w io.Writer, rep *DoctorReport) {
	fmt.Fprintf(w, "pi-llm-gateway doctor — %s\n\n", rep.Dir)
	var fixes []string
	for _, c := range rep.Checks {
		fmt.Fprintf(w, "[%s] %2d. %-18s %s\n", c.Status, c.No, c.Label, c.Detail)
		if c.Fix != "" {
			fixes = append(fixes, fmt.Sprintf("  [%d] %s", c.No, c.Fix))
		}
	}
	fmt.Fprintf(w, "\nresult: %d ok / %d warn / %d fail / %d skip\n",
		rep.Count(CheckOK), rep.Warnings(), rep.Failures(), rep.Count(CheckSkip))
	if len(fixes) > 0 {
		fmt.Fprintln(w, "Fix:")
		for _, f := range fixes {
			fmt.Fprintln(w, f)
		}
	}
}

// --- 1. runtime dir / setup.json -------------------------------------------

func doctorRuntimeDir(paths setup.Paths, st *setup.State, stateErr error) Check {
	c := Check{No: 1, Label: "runtime dir"}
	if _, err := os.Stat(paths.Dir); err != nil {
		c.Status = CheckFail
		c.Detail = fmt.Sprintf("not found: %s", paths.Dir)
		c.Fix = "run `gateway` (no arguments) to start setup"
		return c
	}
	if stateErr != nil {
		c.Status = CheckFail
		c.Detail = fmt.Sprintf("setup.json unreadable: %v", stateErr)
		c.Fix = "fix or remove setup.json, then re-run `gateway`"
		return c
	}
	if st == nil {
		// 手動構築形(§5: config+secrets 揃いでセットアップ済み扱い)
		if setup.IsComplete(paths, nil) {
			c.Status = CheckWarn
			c.Detail = "manual layout (setup.json absent, config+secrets found)"
			c.Fix = "re-run `gateway` to write setup.json (enables doctor/updates)"
		} else {
			c.Status = CheckFail
			c.Detail = "no setup.json and no config+secrets"
			c.Fix = "run `gateway` (no arguments) to start setup"
		}
		return c
	}
	provs := ""
	if len(st.Providers) > 0 {
		provs = ", providers=" + strings.Join(st.Providers, ",")
	}
	return Check{No: 1, Status: CheckOK, Label: "runtime dir",
		Detail: fmt.Sprintf("ready (setup.json v%d, port %d, startup=%s%s)",
			st.Version, st.Port, startupLabel(st.Startup), provs)}
}

func startupLabel(s string) string {
	if s == "" {
		return "manual"
	}
	return s
}

// --- 2. config/models.yaml ---------------------------------------------------

func doctorConfig(paths setup.Paths, st *setup.State) Check {
	cfg, err := config.Load(resolveConfigPath(paths))
	if err != nil {
		return Check{No: 2, Status: CheckFail, Label: "config/models.yaml",
			Detail: err.Error(),
			Fix:    "re-run `gateway` for a fresh config, or fix the alias table",
		}
	}
	aliases := make([]string, 0, len(cfg.Models))
	for a := range cfg.Models {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	// 同一実モデルが複数エントリ(別provider重複)の警告 — ResolveRequestは辞書順1件を
	// 採用するため、どちらが使われるか分からなくなる。doctorで可視化する
	byModel := map[string][]string{}
	for key, mc := range cfg.Models {
		byModel[mc.Model] = append(byModel[mc.Model], key)
	}
	var dups []string
	for m, ks := range byModel {
		if len(ks) > 1 {
			sort.Strings(ks)
			dups = append(dups, fmt.Sprintf("%s (%s)", m, strings.Join(ks, ", ")))
		}
	}
	sort.Strings(dups)
	if missing := stateAliasDrift(st, aliases); len(missing) > 0 {
		return Check{No: 2, Status: CheckWarn, Label: "config/models.yaml",
			Detail: fmt.Sprintf("%d aliases parsed (default=%s), but setup.json lacks: %s",
				len(aliases), cfg.DefaultModel, strings.Join(missing, ", ")),
			Fix: "re-run `gateway` (updates setup.json bookkeeping)",
		}
	}
	if len(dups) > 0 {
		return Check{No: 2, Status: CheckWarn, Label: "config/models.yaml",
			Detail: fmt.Sprintf("%d aliases, default=%s — duplicate real model: %s",
				len(aliases), cfg.DefaultModel, strings.Join(dups, "; ")),
			Fix: "keep one entry (menu 3) — requests for that model resolve to the first entry",
		}
	}
	return Check{No: 2, Status: CheckOK, Label: "config/models.yaml",
		Detail: fmt.Sprintf("%d aliases, default=%s, providers=%d",
			len(aliases), cfg.DefaultModel, len(cfg.Providers))}
}

// stateAliasDrift は setup.json の aliases 記録と実configの差分を返す。
func stateAliasDrift(st *setup.State, aliases []string) []string {
	if st == nil || len(st.Aliases) == 0 {
		return nil // 記録無しは旧環境なので差分を見ない
	}
	set := map[string]bool{}
	for _, a := range st.Aliases {
		set[a] = true
	}
	var missing []string
	for _, a := range aliases {
		if !set[a] {
			missing = append(missing, a)
		}
	}
	sort.Strings(missing)
	return missing
}

// resolveConfigPath は runtime config を優先し、無ければ CONFIG_FILE env を見る。
func resolveConfigPath(paths setup.Paths) string {
	if _, err := os.Stat(paths.Config); err == nil {
		return paths.Config
	}
	if v := os.Getenv("CONFIG_FILE"); v != "" {
		return v
	}
	return paths.Config
}

// --- 3. secrets 復可 + 鍵齢 ---------------------------------------------------

func doctorSecrets(paths setup.Paths, st *setup.State, opts DoctorOptions) Check {
	c := Check{No: 3, Label: "secrets"}
	if opts.LoadSecrets == nil {
		c.Status = CheckSkip
		c.Detail = "no decrypt resolver configured"
		return c
	}
	sec, err := opts.LoadSecrets()
	if err != nil {
		c.Status = CheckFail
		c.Detail = "cannot decrypt: " + err.Error()
		c.Fix = "check identity sources (AGE_IDENTITY_FILE / AGE_IDENTITY / SECRETS_DECRYPT_CMD, AGE_PASSPHRASE) or re-run `gateway`"
		return c
	}

	keys := sec.AllGatewayKeys()
	mode := "file"
	if st != nil && st.IdentityMode != "" {
		mode = st.IdentityMode
	}
	detail := fmt.Sprintf("decrypted ok — %d gateway key(s), %d recipient(s), identity_mode=%s",
		len(keys), len(sec.IdentityRecipients), mode)
	if age, stale, max := secretsAge(paths.Secrets, opts.MaxAgeDays); max > 0 {
		detail += fmt.Sprintf(", key age %dd (max %dd)", age, max)
		if stale {
			c.Status = CheckWarn
			c.Detail = detail
			c.Fix = "re-encrypt via `gateway secrets rotate -recipient <age1...>` (refreshes the file age)"
			return c
		}
	}
	c.Status = CheckOK
	c.Detail = detail
	return c
}

// secretsAge は暗号化secretsの鍵齢(mtime起点・日数)を返す。
func secretsAge(path string, maxAgeDays int64) (days int, stale bool, max int64) {
	max = maxAgeDays
	if max <= 0 {
		return 0, false, 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false, max
	}
	age := int64(time.Since(fi.ModTime()).Hours() / 24)
	if age < 0 {
		age = 0
	}
	return int(age), age > max, max
}

// --- 4/5. gateway_keys ↔ pi models.json ---------------------------------------

func doctorPi(paths setup.Paths, port, piPath string, opts DoctorOptions) []Check {
	cKey := Check{No: 4, Label: "pi gateway key"}
	cDrift := Check{No: 5, Label: "pi alias drift"}

	data, err := os.ReadFile(piPath)
	if err != nil {
		cKey.Status, cKey.Detail, cKey.Fix = CheckWarn,
			fmt.Sprintf("pi models.json not found (%s)", piPath),
			"register pi: `gateway pi install` (menu 4)"
		cDrift.Status, cDrift.Detail = CheckSkip, "pi models.json not found"
		return []Check{cKey, cDrift}
	}
	var pi piModels
	if err := json.Unmarshal(data, &pi); err != nil {
		cKey.Status, cKey.Detail, cKey.Fix = CheckWarn,
			fmt.Sprintf("%s is not valid JSON: %v", piPath, err),
			"back up and re-register pi: `gateway pi install` (menu 4)"
		cDrift.Status, cDrift.Detail = CheckSkip, "pi models.json unparsable"
		return []Check{cKey, cDrift}
	}
	gw := pi.Providers["gateway"]
	if gw == nil {
		cKey.Status, cKey.Detail, cKey.Fix = CheckWarn,
			"providers.gateway block absent from pi models.json",
			"register pi: `gateway pi install` (menu 4)"
		cDrift.Status, cDrift.Detail = CheckSkip, "no gateway block in pi models.json"
		return []Check{cKey, cDrift}
	}

	// 4. キー一致: secretsのどれかと一致するか(定数時間比較)
	if opts.LoadSecrets == nil {
		cKey.Status, cKey.Detail = CheckSkip, "cannot verify pi apiKey (no decrypt resolver)"
	} else if sec, err := opts.LoadSecrets(); err != nil {
		cKey.Status, cKey.Detail = CheckWarn, "cannot verify pi apiKey: secrets undecryptable"
	} else if !strings.HasPrefix(gw.APIKey, "gk-") {
		cKey.Status, cKey.Detail, cKey.Fix = CheckWarn,
			"pi apiKey is not a gateway key (gk-…)",
			"re-register pi: `gateway pi install` (menu 4)"
	} else {
		found := false
		for _, k := range sec.AllGatewayKeys() {
			if subtle.ConstantTimeCompare([]byte(k), []byte(gw.APIKey)) == 1 {
				found = true
				break
			}
		}
		if found {
			cKey.Status, cKey.Detail = CheckOK,
				fmt.Sprintf("matches secrets (fp %s), %d key(s) installed", fp(gw.APIKey), len(sec.AllGatewayKeys()))
		} else {
			cKey.Status, cKey.Detail, cKey.Fix = CheckFail,
				"pi apiKey does not match any secret gateway key",
				"accept the pi-side key into secrets (`gateway keys add`), or re-register pi: `gateway pi install` (menu 4)"
		}
	}

	// 5. drift(pi id ↔ config): 新規約は pi id = 実モデル名。legacyエイリアスは情報表示
	cfg, err := config.Load(resolveConfigPath(paths))
	if err != nil {
		cDrift.Status, cDrift.Detail = CheckSkip, "config unparsable (see check 2)"
		return []Check{cKey, cDrift}
	}
	want := cfg.ModelNameSet()
	have := map[string]bool{}
	for _, m := range gw.Models {
		if m.ID != "" {
			have[m.ID] = true
		}
	}
	var missing, extra []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
		delete(have, w)
	}
	for e := range have {
		extra = append(extra, e)
	}
	sort.Strings(extra)
	wantURL := fmt.Sprintf("http://127.0.0.1:%s/v1", port)
	legacyNote := ""
	if legacy := cfg.LegacyAliases(); len(legacy) > 0 {
		legacyNote = fmt.Sprintf(", %d legacy alias(es) kept for compatibility", len(legacy))
	}
	switch {
	case len(missing) > 0 || len(extra) > 0:
		var parts []string
		if len(missing) > 0 {
			parts = append(parts, fmt.Sprintf("missing in pi: %s", strings.Join(missing, ", ")))
		}
		if len(extra) > 0 {
			parts = append(parts, fmt.Sprintf("stale in pi: %s", strings.Join(extra, ", ")))
		}
		cDrift.Status, cDrift.Detail, cDrift.Fix = CheckWarn,
			strings.Join(parts, "; "),
			"re-register pi: `gateway pi install` (menu 4)"
	case gw.BaseURL != wantURL:
		cDrift.Status, cDrift.Detail, cDrift.Fix = CheckWarn,
			fmt.Sprintf("baseUrl %s does not point at this gateway (%s)", gw.BaseURL, wantURL),
			"re-register pi: `gateway pi install` (menu 4)"
	default:
		cDrift.Status, cDrift.Detail = CheckOK,
			fmt.Sprintf("%d/%d model ids match (id = model name)%s, baseUrl ok", len(want), len(gw.Models), legacyNote)
	}
	return []Check{cKey, cDrift}
}

// piModels は pi 側 models.json から doctor が必要な形だけ取り出す構造。
type piModels struct {
	Providers map[string]*piGateway `json:"providers"`
}

type piGateway struct {
	APIKey  string         `json:"apiKey"`
	BaseURL string         `json:"baseUrl"`
	Models  []piModelEntry `json:"models"`
}

type piModelEntry struct {
	ID string `json:"id"`
}

// defaultPiModelsPath は ~/.pi/agent/models.json(GATEWAY_PI_MODELS フック込み)。
func defaultPiModelsPath() string {
	if v := os.Getenv("GATEWAY_PI_MODELS"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pi/agent/models.json"
	}
	// Windowsパス区切りを含んでも表示が壊れないよう正規化は行わない(そのまま結合)
	return home + string(os.PathSeparator) + ".pi" + string(os.PathSeparator) + "agent" + string(os.PathSeparator) + "models.json"
}

// --- 6. 稼働状態(cold 常駐は正常表示) ---------------------------------------------

func doctorRunning(opts DoctorOptions, port string) (Check, bool) {
	c := Check{No: 6, Label: "gateway process"}
	url := doctorHealthURL(opts.Addr, port)
	hz, err := fetchDoctorHealth(url)
	if err != nil {
		c.Status = CheckOK
		c.Detail = fmt.Sprintf("cold — not running at %s (normal; `gateway up` or the first request warms it)", url)
		return c, false
	}
	c.Status = CheckOK
	c.Detail = fmt.Sprintf("warm at %s — uptime %ds, %d aliases, default=%s",
		url, hz.UptimeSeconds, len(hz.Models), hz.DefaultModel)
	return c, true
}

func doctorHealthURL(addrOverride, port string) string {
	if v := addrOverride; v != "" {
		return strings.TrimRight(v, "/")
	}
	for _, env := range []string{"GATEWAY_ADDR", "GATEWAY_RELOAD_ADDR"} {
		if v := os.Getenv(env); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "http://127.0.0.1:" + port
}

type doctorHealth struct {
	Status        string         `json:"status"`
	UptimeSeconds int            `json:"uptime_seconds"`
	DefaultModel  string         `json:"default_model"`
	Models        map[string]any `json:"models"`
}

func fetchDoctorHealth(url string) (*doctorHealth, error) {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(url + "/healthz")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	var out doctorHealth
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- 7. 自動起動(第3バッチで実体実装。ここでは現状の見え方のみ) ----------------------

func doctorAutostart(paths setup.Paths, st *setup.State) Check {
	c := Check{No: 7, Label: "autostart"}
	if _, err := os.Stat(paths.Launcher); err != nil {
		c.Status = CheckWarn
		c.Detail = fmt.Sprintf("launcher missing: %s", paths.Launcher)
		c.Fix = "re-run `gateway` (writes the up launcher)"
		return c
	}
	if info, ok := autostartProbe(); ok {
		c.Status = CheckOK
		c.Detail = "registered — " + info
		return c
	}
	c.Status = CheckOK
	c.Detail = "manual — start with the launcher (" + filepathBase(paths.Launcher) + ") or `gateway up`; login autostart arrives in a later update"
	return c
}

// --- 8. ポート競合 / BIND ------------------------------------------------------------

func doctorPortBind(paths setup.Paths, st *setup.State, port string, warm bool) Check {
	c := Check{No: 8, Label: "port & bind"}
	bind := envOrStr("BIND", "")
	if st != nil && st.Bind != "" && bind == "" {
		bind = st.Bind // env優先(実サーバーと同じ解決順)
	}
	if bind == "" {
		if lb, ok := launcherBind(paths); ok && lb != "" {
			bind = lb
		}
	}
	if bind == "" {
		bind = "127.0.0.1" // BIND未指定時のサーバー既定(実効値)
	}
	warn := ""
	if bind == "0.0.0.0" || bind == "::" {
		warn = "listens on all interfaces; 127.0.0.1 recommended"
	}
	if warm {
		if warn != "" {
			c.Status = CheckWarn
			c.Detail = fmt.Sprintf("port %s in use by this gateway (warm) — %s", port, warn)
			c.Fix = "reconfigure with BIND=127.0.0.1 (`gateway init --bind` or env) for loopback-only"
			return c
		}
		c.Status = CheckOK
		c.Detail = fmt.Sprintf("port %s in use by this gateway (warm), bind=%s (loopback)", port, bind)
		return c
	}
	if occupied(doctorBindForProbe(bind), port) {
		c.Status = CheckWarn
		c.Detail = fmt.Sprintf("port %s busy — another process is listening (gateway itself is cold)", port)
		c.Fix = "stop it, or change the port (PORT env / `gateway init --port`)"
		return c
	}
	if warn != "" {
		c.Status = CheckWarn
		c.Detail = fmt.Sprintf("port %s free, %s", port, warn)
		c.Fix = "reconfigure BIND=127.0.0.1 (env or `gateway init --bind`)"
		return c
	}
	c.Status = CheckOK
	c.Detail = fmt.Sprintf("port %s free, bind=%s (loopback only)", port, bind)
	return c
}

func nonEmptyBind(bind string) string {
	if bind == "" {
		return "127.0.0.1"
	}
	return bind
}

// launcherBind はランチャからのBIND抽出(doctorPortBind用)。
func launcherBind(paths setup.Paths) (string, bool) {
	_, bind := launcherEnv(paths)
	return bind, bind != ""
}

// doctorBindForProbe は競合プローブ用のリッスンアドレス。
func doctorBindForProbe(bind string) string {
	switch bind {
	case "", "0.0.0.0", "::":
		return "127.0.0.1"
	}
	return bind
}

func occupied(host, port string) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

// --- 9. バイナリ版差(アップデート忘れ検知) --------------------------------------------

func doctorBinary(paths setup.Paths, opts DoctorOptions) []Check {
	c := Check{No: 9, Label: "runtime binary"}
	if _, err := os.Stat(paths.Bin); err != nil {
		c.Status = CheckFail
		c.Detail = fmt.Sprintf("runtime binary missing: %s", paths.Bin)
		c.Fix = "run `gateway` (copies the current binary into the runtime)"
		return []Check{c}
	}
	binVer, err := runtimeBinVersion(paths.Bin, opts.BinVersion)
	if err != nil {
		c.Status = CheckWarn
		c.Detail = fmt.Sprintf("cannot read runtime version (%s): %v", paths.Bin, err)
		c.Fix = "re-run `gateway` to refresh the runtime binary"
		return []Check{c}
	}
	if samePath(paths.Bin) {
		c.Status = CheckOK
		c.Detail = fmt.Sprintf("v%s (the binary you are running)", binVer)
		return []Check{c}
	}
	if opts.CLIVersion != "" && binVer != opts.CLIVersion {
		c.Status = CheckWarn
		c.Detail = fmt.Sprintf("runtime bin v%s vs CLI v%s — update not applied yet", binVer, opts.CLIVersion)
		c.Fix = "run `gateway` (re-running init refreshes bin; on Windows the refresh lands on the next launcher start)"
		return []Check{c}
	}
	c.Status = CheckOK
	c.Detail = fmt.Sprintf("v%s (bin matches CLI)", binVer)
	return []Check{c}
}

// samePath は現在の実行ファイルが runtime bin と同一か。
func samePath(bin string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	ai, errA := os.Stat(exe)
	bi, errB := os.Stat(bin)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// runtimeBinVersion は注入があればそれを、なければ実行で版を取る。
func runtimeBinVersion(bin string, injected func(string) (string, error)) (string, error) {
	if injected != nil {
		return injected(bin)
	}
	return execBinVersion(bin)
}

// execBinVersion は runtime bin を `-v` で動かして版文字列を取る。
// `-v` は起動直後に出して返るだけで、サーバーやウィザードは動き出さない。
func execBinVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-v")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) == 0 {
		return "", fmt.Errorf("no output")
	}
	ver := strings.TrimPrefix(f[len(f)-1], "v")
	if ver == "" || strings.ContainsAny(ver, ".") && !isNumericDotted(ver) {
		return "", fmt.Errorf("unexpected version output %q", f[len(f)-1])
	}
	return ver, nil
}

func isNumericDotted(s string) bool {
	for _, part := range strings.Split(s, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// --- 10. Windows console codepage -------------------------------------------------------

func doctorCodepage() Check {
	c := Check{No: 10, Label: "console codepage"}
	if runtime.GOOS != "windows" {
		c.Status = CheckSkip
		c.Detail = fmt.Sprintf("(n/a on %s)", runtime.GOOS)
		return c
	}
	cp, ok := consoleOutputCodePage()
	if !ok {
		c.Status = CheckSkip
		c.Detail = "no console (headless run)"
		return c
	}
	if cp != 65001 {
		c.Status = CheckWarn
		c.Detail = fmt.Sprintf("console codepage is %d (not UTF-8) — Japanese output may be garbled (gateway always writes UTF-8; cp932 conversion is not used)", cp)
		c.Fix = "run `chcp 65001` or use Windows Terminal"
		return c
	}
	c.Status = CheckOK
	c.Detail = "65001 (UTF-8) — Japanese output is safe"
	return c
}

// --- 共有小関数 -----------------------------------------------------------------------------

// doctorPort は接続先ポートを決める(PORT env > setup.json > ランチャ > 8080。
// メニューと同一の規約に、setup.json無しのmanual構成用にランチャ解析を追加)。
func doctorPort(paths setup.Paths) string {
	if v := envOrStr("PORT", ""); v != "" {
		return v
	}
	if st, err := setup.LoadState(paths); err == nil && st != nil && st.Port != 0 {
		return fmt.Sprint(st.Port)
	}
	port, _ := launcherEnv(paths)
	if port != "" {
		return port
	}
	return "8080"
}

// launcherEnv は up.cmd / up.sh から PORT= / BIND= を抽出する。
// wizard以前の手動構成(setup.json無し)では起動値がランチャにしかないため。
func launcherEnv(paths setup.Paths) (port, bind string) {
	data, err := os.ReadFile(paths.Launcher)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "set ") // cmd: set PORT=18080
		if rest, ok := strings.CutPrefix(line, "PORT="); ok && port == "" {
			port = strings.Trim(rest, "\" ")
		}
		if rest, ok := strings.CutPrefix(line, "BIND="); ok && bind == "" {
			bind = strings.Trim(rest, "\" ")
		}
	}
	return port, bind
}

func envOrStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
