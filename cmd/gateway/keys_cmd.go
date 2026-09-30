// gateway keys — secrets.yaml.age の鍵をCLIで管理するサブコマンド。
//
// 使い方:
//
//	gateway keys list                      鍵の棚卸し(値そのものは表示しない)
//	gateway keys add [VALUE]               gatewayキーを追加(省略時はランダム生成して表示)
//	gateway keys remove VALUE              gatewayキーを削除(完全一致 or ユニーク前方一致)
//	gateway keys set PROVIDER [VALUE|-]    api_keys.PROVIDER を設定(省略/- は標準入力)
//	gateway keys unset PROVIDER            api_keys.PROVIDER を削除
//
// 共通フラグ:
//
//	-reload                  保存後に稼働中gatewayへ POST /admin/reload を送る
//	-addr URL                再読み込み先(既定 http://127.0.0.1:18080)
//	-recipient age1...       SECRETS_DECRYPT_CMD使用時の再暗号化先(複数回指定可)
//
// 環境変数はサーバーと同じもの(SECRETS_FILE / AGE_IDENTITY_FILE / AGE_IDENTITY /
// AGE_IDENTITY_CMD / AGE_PASSPHRASE / SECRETS_DECRYPT_CMD)を共有する。
//
// セキュリティ方針:
//   - 復号済みSecretsはプロセス終了時にZero()(サーバーと同じ経路)
//   - list/add の出力は値そのものではなく指紋(先頭4文字 + SHA-256短縮)を表示する
//     ただしランダム生成時に限り、新キーを一度だけ明示する(保管は運用者の責務)
//   - 最後のgatewayキーの削除はロックアウト防止のため拒否する
//   - SECRETS_DECRYPT_CMD は復号済み平文しか得られず再暗号化用の公開鍵が分からないため、
//     identity系env経由の復号でない場合は -recipient が必須
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/awnumar/memguard"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

// multiFlag は -recipient のように複数回指定できるフラグ。
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// keyFlags は keys サブコマンドの共通フラグ。テストから直接構築できる。
type keyFlags struct {
	reload     bool
	addr       string
	recipients multiFlag
}

// defaultReloadAddr はホットリロード通知の既定URL。
const defaultReloadAddr = "http://127.0.0.1:18080"

var providerNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

func keysCmd(rest []string) error {
	if len(rest) == 0 {
		printKeysUsage(os.Stdout)
		return errors.New("keysのサブコマンドを指定してください(list / add / remove / set / unset)")
	}
	kf, op, args, help, err := extractKeyFlags(rest)
	if err != nil {
		printKeysUsage(os.Stdout)
		return err
	}
	if help {
		printKeysUsage(os.Stdout)
		return nil
	}
	defer memguard.Purge()
	return runKeysOp(slog.Default(), op, kf, args, os.Stdin, os.Stdout)
}

// extractKeyFlags は rest から共通フラグ(-reload / -addr / -recipient / -h)を
// 任意の位置で抽出し、残りを (サブコマンド, 位置引数) として返す。
// 標準flagパッケージは最初の非フラグ引数で解析が止まるため
// (例: `keys add VALUE -reload` で -reload が消失)、ここで自前で走査する。
func extractKeyFlags(rest []string) (kf *keyFlags, op string, args []string, help bool, err error) {
	kf = &keyFlags{}
	var positional []string
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		switch {
		case tok == "--":
			positional = append(positional, rest[i+1:]...)
			i = len(rest)
		case tok == "-h" || tok == "-help" || tok == "--help":
			help = true
		case tok == "-reload" || tok == "--reload":
			kf.reload = true
		case tok == "-addr" || tok == "--addr":
			if i+1 >= len(rest) {
				return nil, "", nil, false, fmt.Errorf("-addr の値がありません")
			}
			kf.addr = rest[i+1]
			i++
		case strings.HasPrefix(tok, "-addr="):
			kf.addr = strings.TrimPrefix(tok, "-addr=")
		case strings.HasPrefix(tok, "--addr="):
			kf.addr = strings.TrimPrefix(tok, "--addr=")
		case tok == "-recipient" || tok == "--recipient":
			if i+1 >= len(rest) {
				return nil, "", nil, false, fmt.Errorf("-recipient の値がありません")
			}
			kf.recipients.Set(rest[i+1])
			i++
		case strings.HasPrefix(tok, "-recipient="):
			_ = kf.recipients.Set(strings.TrimPrefix(tok, "-recipient="))
		case strings.HasPrefix(tok, "--recipient="):
			_ = kf.recipients.Set(strings.TrimPrefix(tok, "--recipient="))
		case strings.HasPrefix(tok, "-"):
			return nil, "", nil, false, fmt.Errorf("未知のフラグ %q", tok)
		default:
			positional = append(positional, tok)
		}
	}
	if len(positional) == 0 && !help {
		return nil, "", nil, false, errors.New("keysのサブコマンドを指定してください(list / add / remove / set / unset)")
	}
	if len(positional) > 0 {
		op = positional[0]
		args = positional[1:]
	}
	if kf.addr == "" {
		kf.addr = envOr("GATEWAY_RELOAD_ADDR", defaultReloadAddr)
	}
	return kf, op, args, help, nil
}

func printKeysUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway keys <subcommand> [args] [flags]

subcommands:
  list                      鍵の棚卸し(値は指紋のみ表示)
  add [VALUE]               gatewayキーを追加(省略時はランダム生成して表示)
  remove VALUE              gatewayキーを削除(完全一致 or ユニーク前方一致)
  set PROVIDER [VALUE|-]    api_keys.PROVIDER を設定(省略/- で標準入力から1行)
  unset PROVIDER            api_keys.PROVIDER のキーを削除

flags:
  -reload                   保存後に稼働中gatewayへ再読み込み(POST /admin/reload)を通知
  -addr URL                 再読み込み先(既定 `+defaultReloadAddr+`)
  -recipient age1...        SECRETS_DECRYPT_CMD使用時の再暗号化先(複数回指定可)

env(サーバーと共通): SECRETS_FILE, AGE_IDENTITY_FILE, AGE_IDENTITY,
AGE_IDENTITY_CMD, AGE_PASSPHRASE, SECRETS_DECRYPT_CMD
`)
}

// runKeysOp は keys サブコマンドの本体。テストから直接呼べるよう入出力を受け取る。
// 編集系(add/remove/set/unset)は「復号 → 変更 → 再暗号化保存 → (任意)再読み込み通知」の順に進む。
func runKeysOp(log *slog.Logger, op string, kf *keyFlags, args []string, stdin io.Reader, stdout io.Writer) error {
	if kf == nil {
		kf = &keyFlags{}
	}

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
			return err
		}
		idSrc, closeFn, err := ident.open()
		if err != nil {
			return err
		}
		if closeFn != nil {
			defer closeFn()
		}
		opts.Identity = idSrc
	}
	sec, err := secrets.Load(opts)
	if err != nil {
		return fmt.Errorf("secretsの復号に失敗: %w", err)
	}
	defer sec.Zero()

	switch op {
	case "list":
		return opList(sec, secretsFile, stdout)
	case "add", "remove", "set", "unset":
	default:
		printKeysUsage(stdout)
		return fmt.Errorf("未知のkeysサブコマンド %q", op)
	}

	var changed bool
	switch op {
	case "add":
		changed, err = opAdd(sec, args, stdin, stdout)
	case "remove":
		err = opRemove(sec, args, stdout)
		changed = true
	case "set":
		changed, err = opSet(sec, args, stdin, stdout)
	case "unset":
		changed, err = opUnset(sec, args, stdout)
	}
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return finishKeysOp(log, sec, secretsFile, kf, stdout)
}

// finishKeysOp は変更後のSecretsを再暗号化保存し、必要なら再読み込みを通知する。
func finishKeysOp(_ *slog.Logger, sec *secrets.Secrets, secretsFile string, kf *keyFlags, stdout io.Writer) error {
	recips := sec.IdentityRecipients
	if len(recips) == 0 {
		recips = kf.recipients
	}
	if len(recips) == 0 {
		return errors.New("再暗号化のrecipientが決まりません: -recipient age1... を指定するか、identity経由(AGE_IDENTITY_FILE 等)で実行してください")
	}
	if err := secrets.Save(secretsFile, sec, recips); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "保存しました: %s\n", secretsFile)
	if kf.reload {
		keys := sec.AllGatewayKeys()
		if len(keys) == 0 {
			fmt.Fprintln(stdout, "警告: gatewayキーが0件のため再読み込みを通知できません")
			return nil
		}
		if err := triggerReload(kf.addr, keys[0]); err != nil {
			fmt.Fprintf(stdout, "再読み込みに失敗しました(gateway未起動の可能性): %v\n鍵の変更は保存済みです。次回起動時に読み込まれます。\n", err)
			return nil
		}
		fmt.Fprintf(stdout, "稼働中のgatewayに再読み込みを通知しました(POST %s/admin/reload)\n", strings.TrimRight(kf.addr, "/"))
	}
	return nil
}

// opAdd はgatewayキーを1つ追加する(値未指定時はランダム生成)。
func opAdd(sec *secrets.Secrets, args []string, stdin io.Reader, stdout io.Writer) (changed bool, err error) {
	var v string
	generated := false
	switch {
	case len(args) == 0:
		v = newGatewayKey()
		generated = true
	case len(args) == 1:
		v = strings.TrimSpace(args[0])
		if v == "-" {
			if v, err = readStdinLine(stdin); err != nil {
				return false, err
			}
			v = strings.TrimSpace(v)
		}
	default:
		return false, errors.New("引数が多すぎます: gateway keys add [VALUE]")
	}
	if v == "" {
		return false, errors.New("追加するgatewayキーが空です")
	}
	if hasGatewayKey(sec, v) {
		fmt.Fprintf(stdout, "そのキーは既に登録されています(%s)(変更なし)\n", maskKey(v))
		return false, nil
	}
	if sec.GatewayKey == "" && len(sec.GatewayKeys) == 0 {
		sec.GatewayKey = v // 最初の1本はレガシー単一キー形式を維持
	} else {
		sec.GatewayKeys = append(sec.GatewayKeys, v)
	}
	fmt.Fprintf(stdout, "gatewayキーを追加しました: %s\n", maskKey(v))
	if generated {
		fmt.Fprintf(stdout, "生成されたキー(今だけ表示します。安全な場所に保管してください):\n  %s\n", v)
	}
	return true, nil
}

// opRemove はgatewayキーを削除する。完全一致→ユニーク前方一致の順で解決する。
func opRemove(sec *secrets.Secrets, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: gateway keys remove VALUE")
	}
	v := args[0]
	keys := sec.AllGatewayKeys()
	var match string
	if containsStr(keys, v) {
		match = v
	} else {
		if len(v) < 4 {
			return fmt.Errorf("gatewayキー %q は見つかりません(前方一致を使う場合は4文字以上)", v)
		}
		var m []string
		for _, k := range keys {
			if strings.HasPrefix(k, v) {
				m = append(m, k)
			}
		}
		switch len(m) {
		case 0:
			return errors.New("削除対象のgatewayキーが見つかりません")
		case 1:
			match = m[0]
		default:
			return fmt.Errorf("前方一致 %q に %d 件が該当し曖昧です: より長く指定するか、全文で指定してください", v, len(m))
		}
	}

	remaining := removeStrAll(keys, match)
	if len(remaining) == 0 {
		return errors.New("このキーを削除すると有効なgatewayキーが0件になります(ロックアウト防止)。先に別のキーを `gateway keys add` してください")
	}
	if sec.GatewayKey == match {
		sec.GatewayKey = ""
	} else {
		sec.GatewayKeys = removeStrAll(sec.GatewayKeys, match)
	}
	fmt.Fprintf(stdout, "gatewayキーを削除しました: %s\n", maskKey(match))
	return nil
}

// opSet は api_keys.<provider> を設定する。
func opSet(sec *secrets.Secrets, args []string, stdin io.Reader, stdout io.Writer) (changed bool, err error) {
	if len(args) == 0 {
		return false, errors.New("usage: gateway keys set PROVIDER [VALUE|-]")
	}
	name := args[0]
	if !providerNameRe.MatchString(name) {
		return false, fmt.Errorf("provider名が不正です %q(半角英数字と - _ のみ)", name)
	}
	var v string
	if len(args) >= 2 {
		v = strings.TrimSpace(args[1])
	}
	if v == "-" || v == "" {
		v, err = readStdinLine(stdin)
		if err != nil {
			return false, err
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return false, fmt.Errorf("provider %q のキーが空です(標準入力が空)", name)
		}
	}
	if len(sec.APIKeys) == 0 {
		sec.APIKeys = map[string]string{}
	}
	if cur := sec.APIKeys[name]; cur == v {
		fmt.Fprintf(stdout, "変更なし: api_keys.%s は設定済みです(%s)\n", name, maskKey(v))
		return false, nil
	}
	sec.APIKeys[name] = v
	fmt.Fprintf(stdout, "api_keys.%s を設定しました(%s)\n", name, maskKey(v))
	return true, nil
}

// opUnset は api_keys.<provider> を削除する。
func opUnset(sec *secrets.Secrets, args []string, stdout io.Writer) (changed bool, err error) {
	if len(args) != 1 {
		return false, errors.New("usage: gateway keys unset PROVIDER")
	}
	name := args[0]
	if !providerNameRe.MatchString(name) {
		return false, fmt.Errorf("provider名が不正です %q(半角英数字と - _ のみ)", name)
	}
	if _, ok := sec.APIKeys[name]; !ok {
		fmt.Fprintf(stdout, "設定がありません: api_keys.%s(変更なし)\n", name)
		return false, nil
	}
	delete(sec.APIKeys, name)
	fmt.Fprintf(stdout, "api_keys.%s を削除しました(該当providerへのリクエストは次の再読み込み後に失敗します)\n", name)
	return true, nil
}

// opList は鍵の棚卸しを出力する(値そのものは表示しない)。
func opList(sec *secrets.Secrets, secretsFile string, stdout io.Writer) error {
	if days, ok, err := secretsFileAgeDays(secretsFile); err != nil {
		return err
	} else if ok {
		fmt.Fprintf(stdout, "secrets: %s(最終更新から %d 日)\n", secretsFile, days)
	} else {
		fmt.Fprintf(stdout, "secrets: %s(存在しない?)\n", secretsFile)
	}
	for i, r := range sec.IdentityRecipients {
		fmt.Fprintf(stdout, "identity 公開鍵 #%d: %s\n", i+1, r)
	}
	keys := sec.AllGatewayKeys()
	fmt.Fprintf(stdout, "gatewayキー: %d 件\n", len(keys))
	for i, k := range keys {
		fmt.Fprintf(stdout, "  [%d] %s\n", i, maskKey(k))
	}
	names := make([]string, 0, len(sec.APIKeys))
	for n := range sec.APIKeys {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(stdout, "api_keys(provider上流キー):")
	for _, n := range names {
		fmt.Fprintf(stdout, "  %-14s 設定済み(%s)\n", n, maskKey(sec.APIKeys[n]))
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "  (なし — configでproviderを参照している場合、該当リクエストが失敗します)")
	}
	return nil
}

// secretsFileAgeDays はsecretsファイルの最終更新からの日数を返す。
// 対象が存在しない場合は ok=false。
func secretsFileAgeDays(path string) (int, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return int(time.Since(info.ModTime()).Hours() / 24), true, nil
}

// triggerReload は稼働中gatewayへ POST /admin/reload を送る(gatewayキー認証)。
func triggerReload(addr, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := strings.TrimRight(addr, "/") + "/admin/reload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s へ接続できません: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// newGatewayKey はランダムgatewayキーを生成する(gk- + 128bit Base64URL)。
func newGatewayKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		slog.Error("乱数生成に失敗しました", "error", err)
		os.Exit(1)
	}
	return "gk-" + base64.RawURLEncoding.EncodeToString(b)
}

// maskKey は鍵の指紋(先頭4文字 + SHA-256短縮)を返す。値そのものは出さない。
func maskKey(k string) string {
	if k == "" {
		return "(空)"
	}
	sum := sha256.Sum256([]byte(k))
	prefix := k
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	return fmt.Sprintf("%s…(fp %s)", prefix, hex.EncodeToString(sum[:6]))
}

func hasGatewayKey(sec *secrets.Secrets, v string) bool {
	return sec.GatewayKey == v || containsStr(sec.GatewayKeys, v)
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// removeStrAll はlistからvと完全一致する要素をすべて除去する(新スライスを返す)。
func removeStrAll(list []string, v string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s != v {
			out = append(out, s)
		}
	}
	return out
}

// readStdinLine は入力から1行読む(対話向け。末尾改行は除去)。
func readStdinLine(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("標準入力から1行を読めませんでした")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
