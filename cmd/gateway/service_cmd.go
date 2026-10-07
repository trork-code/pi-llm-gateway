// gateway up / status / down — ゲートウェイの起動・状態確認・停止を1コマンドで行う。
//
// 使い方(pi coding agentの利用を想定):
//
//	gateway up            稼働していなければバックグラウンドで起動→READY表示。
//	                    稼働済みなら何もしない(冪等)。セッション先頭で1回実行すればOK
//	gateway status        稼働状態・構成を表示(未起動でもエラーにしない)
//	gateway down          稼働中gatewayを安全に停止(POST /admin/shutdown)
//
// 接続先URLの決定順: -addr フラグ > env GATEWAY_ADDR > env GATEWAY_RELOAD_ADDR(後方互換)
// > http://127.0.0.1:<env PORT または 8080>
//
// /healthz は認証不要(状態・モデル構成のみ、鍵を出さない)。
// 停止には gatewayキー認証(-key または env GATEWAY_KEY、またはsecretsから自動取得)が必要。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/awnumar/memguard"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

// defaultGatewayPort はサーバー(PORT env)と接続先の共通既定ポート。
const defaultGatewayPort = "8080"

// defaultGatewayLog は`up`で起動したサーバーのログファイル名。
const defaultGatewayLog = "gateway.log"

// serviceFlags は up/status/down の共通フラグ。
type serviceFlags struct {
	addr string // -addr(空ならresolveGatewayURL)
	key  string // down用のgatewayキー
	log  string // up用のログファイル
}

// resolveGatewayURL はゲートウェイの自己接続URLを決める。
// 環境(PORT等)から自動決定するため、運用者は通常フラグ不要。
func resolveGatewayURL(addrOverride string) string {
	if addrOverride != "" && strings.TrimSpace(addrOverride) != "" {
		return strings.TrimRight(addrOverride, "/")
	}
	for _, env := range []string{"GATEWAY_ADDR", "GATEWAY_RELOAD_ADDR"} {
		if v := os.Getenv(env); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "http://127.0.0.1:" + envOr("PORT", defaultGatewayPort)
}

// extractServiceFlags は up/status/down 用の簡易フラグ解析(位置引数の前後どちらでも可)。
func extractServiceFlags(rest []string) (*serviceFlags, error) {
	sf := &serviceFlags{}
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		switch {
		case tok == "--":
			return nil, fmt.Errorf("予期しない位置引数: %q", rest[i+1:])
		case tok == "-h" || tok == "-help" || tok == "--help":
			printServiceUsage(os.Stdout)
			return nil, errHelp
		case tok == "-addr" || tok == "--addr":
			if i+1 >= len(rest) {
				return nil, fmt.Errorf("-addr の値がありません")
			}
			sf.addr = rest[i+1]
			i++
		case strings.HasPrefix(tok, "-addr="):
			sf.addr = strings.TrimPrefix(tok, "-addr=")
		case strings.HasPrefix(tok, "--addr="):
			sf.addr = strings.TrimPrefix(tok, "--addr=")
		case tok == "-key" || tok == "--key":
			if i+1 >= len(rest) {
				return nil, fmt.Errorf("-key の値がありません")
			}
			sf.key = rest[i+1]
			i++
		case strings.HasPrefix(tok, "-key="):
			sf.key = strings.TrimPrefix(tok, "-key=")
		case strings.HasPrefix(tok, "--key="):
			sf.key = strings.TrimPrefix(tok, "--key=")
		case tok == "-log" || tok == "--log":
			if i+1 >= len(rest) {
				return nil, fmt.Errorf("-log の値がありません")
			}
			sf.log = rest[i+1]
			i++
		case strings.HasPrefix(tok, "-log="):
			sf.log = strings.TrimPrefix(tok, "-log=")
		case strings.HasPrefix(tok, "--log="):
			sf.log = strings.TrimPrefix(tok, "--log=")
		default:
			return nil, fmt.Errorf("未知の引数 %q(up/status/down は位置引数を取りません)", tok)
		}
	}
	return sf, nil
}

type serviceOp string

const (
	opUp     serviceOp = "up"
	opStatus serviceOp = "status"
	opDown   serviceOp = "down"
)

// errHelp はヘルプ表示で正常終了したことを示す(sentinel)。
var errHelp = errors.New("help")

// serviceCmd は `gateway up|status|down ...` の本体。
func serviceCmd(op serviceOp, rest []string) error {
	defer memguard.Purge()
	sf, err := extractServiceFlags(rest)
	if err != nil {
		if errors.Is(err, errHelp) {
			return nil
		}
		printServiceUsage(os.Stdout)
		return err
	}
	log := slog.Default()

	url := resolveGatewayURL(sf.addr)
	switch op {
	case opUp:
		return serviceUp(log, url, sf.log, os.Stdout)
	case opStatus:
		return serviceStatus(log, url, os.Stdout)
	case opDown:
		return serviceDown(log, url, sf.key, os.Stdout)
	}
	return fmt.Errorf("未知のservice op %q", op)
}

// healthResponse はサーバー側(/healthz)と同じ応答形状(クライアント側のコピー)。
type healthResponse struct {
	Status        string                      `json:"status"`
	UptimeSeconds int                         `json:"uptime_seconds"`
	PID           int                         `json:"pid"`
	DefaultModel  string                      `json:"default_model"`
	Models        map[string]modelHealthAlias `json:"models"`
	Providers     []string                    `json:"providers"`
	GatewayKeys   int                         `json:"gateway_keys"`
	Recipients    int                         `json:"identity_recipients"`
}

// modelHealthAlias はエイリアス1つ分(client側)。
type modelHealthAlias struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func printServiceUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway up | status | down [flags]

commands:
  up       稼働していなければバックグラウンド起動→READY表示(冪等)
  status   稼働状態・モデル構成を表示
  down     稼働中gatewayを安全に停止

flags:
  -addr URL     ゲートウェイの自己接続先(既定: http://127.0.0.1:<PORT|8080>)
  -key STRING   down用gatewayキー(省略時: env GATEWAY_KEY → secrets復号)
  -log PATH     up時のサーバーログ出力先(既定: gateway.log)

env: GATEWAY_ADDR / GATEWAY_RELOAD_ADDR で接続先を固定化できる
`)
}

// serviceUp は稼働確認→(必要なら)デタッチ起動→READY整備。
func serviceUp(log *slog.Logger, url, logPath string, stdout io.Writer) error {
	if healthOK(url, 800*time.Millisecond) {
		fmt.Fprintf(stdout, "稼働中です: %s(何もしません)\n", url)
		return nil
	}
	if logPath == "" {
		logPath = defaultGatewayLog
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("実行ファイルの特定に失敗: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("ログファイルを開けません(%s): %w", logPath, err)
	}
	defer f.Close()
	cmd := exec.Command(exe)
	cmd.Stdout = f
	cmd.Stderr = f
	detachSysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gatewayの起動に失敗: %w", err)
	}
	_ = cmd.Process.Release() // 親終了後も子を生存させる

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if healthOK(url, 400*time.Millisecond) {
			hz, _ := fetchHealth(url)
			fmt.Fprintf(stdout, "READY: %s/v1(起動からすぐ稼働。pi側でモデルを選択して利用)\n", url)
			if hz != nil {
				fmt.Fprintf(stdout, "  default_model: %s / models: %d / providers: %v\n", hz.DefaultModel, len(hz.Models), hz.Providers)
			}
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("gatewayが%d秒以内にREADYになりませんでした。ログ %s を確認してください", 10, logPath)
}

// serviceStatus は稼働状態を表示する(未起動はエラーにしない)。
func serviceStatus(log *slog.Logger, url string, stdout io.Writer) error {
	if !healthOK(url, 2*time.Second) {
		fmt.Fprintf(stdout, "未起動: %s に応答なし — `gateway up` で起動します\n", url)
		return nil
	}
	hz, err := fetchHealth(url)
	if err != nil {
		fmt.Fprintf(stdout, "status取得に失敗: %v\n", err)
		return nil
	}
	fmt.Fprintf(stdout, "稼働中: %s(起動から %d 秒, pid %d)\n", url, hz.UptimeSeconds, hz.PID)
	fmt.Fprintf(stdout, "default_model: %s\n", hz.DefaultModel)
	aliases := make([]string, 0, len(hz.Models))
	for a := range hz.Models {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, a := range aliases {
		m := hz.Models[a]
		fmt.Fprintf(stdout, "  %-14s → %s/%s\n", a, m.Provider, m.Model)
	}
	fmt.Fprintf(stdout, "providers: %s / gatewayキー: %d 件 / identity: %d\n", strings.Join(hz.Providers, ", "), hz.GatewayKeys, hz.Recipients)
	return nil
}

// serviceDown はPOST /admin/shutdownで安全に停止させる。
func serviceDown(log *slog.Logger, url string, keyArg string, stdout io.Writer) error {
	if !healthOK(url, 2*time.Second) {
		fmt.Fprintf(stdout, "未起動(%s に応答なし)\n", url)
		return nil
	}
	key, err := resolveGatewayKey(log, keyArg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/admin/shutdown", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s/admin/shutdown へ接続できません: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	fmt.Fprintf(stdout, "停止を受け付けました(稼働中リクエスト完了後に停止): %s\n", url)
	return nil
}

// resolveGatewayKey はgatewayキーを決める: フラグ > env GATEWAY_KEY > secrets復号(1本目)。
func resolveGatewayKey(log *slog.Logger, keyArg string) (string, error) {
	if keyArg != "" {
		return keyArg, nil
	}
	if v := os.Getenv("GATEWAY_KEY"); v != "" {
		return v, nil
	}
	log.Debug("secretsからgatewayキーを解決します")
	sec, err := loadSecretsForCLI(log)
	if err != nil {
		return "", fmt.Errorf("gatewayキーが解決できません(-key か env GATEWAY_KEY を指定するか、identity envでsecretsを復号できる状態で実行してください): %w", err)
	}
	keys := sec.AllGatewayKeys()
	defer sec.Zero()
	if len(keys) == 0 {
		return "", errors.New("secretsにgatewayキーがありません")
	}
	return keys[0], nil
}

// healthOK は/healthzへの到達可否を返す。
func healthOK(url string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return resp.StatusCode == http.StatusOK
}

// fetchHealth はhealthz応答を取得する。
func fetchHealth(url string) (*healthResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	var out healthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// loadSecretsForCLI は keys/service CLI用のsecrets復号(サーバーと同じ環境変数と経路)。
func loadSecretsForCLI(log *slog.Logger) (*secrets.Secrets, error) {
	secretsFile := envOr("SECRETS_FILE", "secrets/secrets.yaml.age")
	decryptCmd := os.Getenv("SECRETS_DECRYPT_CMD")
	opts := secrets.LoadOptions{
		EncryptedPath:  secretsFile,
		Passphrase:     os.Getenv("AGE_PASSPHRASE"),
		DecryptCommand: decryptCmd,
	}
	if decryptCmd == "" {
		ident, err := resolveIdentitySource(log)
		if err != nil {
			return nil, err
		}
		idSrc, closeFn, err := ident.open()
		if err != nil {
			return nil, err
		}
		if closeFn != nil {
			defer closeFn()
		}
		opts.Identity = idSrc
	}
	sec, err := secrets.Load(opts)
	if err != nil {
		return nil, fmt.Errorf("secretsの復号に失敗: %w", err)
	}
	return sec, nil
}
