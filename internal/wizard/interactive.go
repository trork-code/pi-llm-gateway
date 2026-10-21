package wizard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// RunInteractive は初回ウィザード(§2.1、5問)を回し、runSetup を呼ぶ。
// 戻り値の startNow は「今すぐ gateway up を起動するか」(cmd層が serviceUp を呼ぶ)。
func RunInteractive(app Options, stdin io.Reader, stdout io.Writer) (*Summary, bool, error) {
	in := bufio.NewReader(stdin)
	fmt.Fprintln(stdout, "Gateway is not set up on this machine.")
	if !askYes(in, stdout, "Start setup now?", true) {
		return nil, false, ErrAborted
	}

	// [1/5] provider
	prov, ok := askProvider(in, stdout)
	if !ok {
		return nil, false, ErrAborted
	}
	app.Provider = prov.ID

	// [2/5] API key
	spec, ok := askAPIKey(in, stdout, prov)
	if !ok {
		return nil, false, ErrAborted
	}
	app.APIKeySpec = spec

	// [3/5] model + alias
	model, alias, ok := askModelAlias(in, stdout, prov)
	if !ok {
		return nil, false, ErrAborted
	}
	app.Model, app.Alias = model, alias

	// [4/5] pi登録
	if !askYes(in, stdout, fmt.Sprintf("Register the `gateway` provider in pi (%s)?", piModelsPath()), true) {
		app.Pi = false
	} else {
		app.Pi = true
	}

	// [5/5] 起動方式
	startup, ok := askStartup(in, stdout)
	if !ok {
		return nil, false, ErrAborted
	}
	app.Startup = startup

	sum, err := RunSetup(app, stdout)
	if err != nil {
		return nil, false, err
	}
	PrintSummary(sum, stdout)

	// 稼働(手動ランチャ経由)。ここは対話のみ: --yes 時はcmd層が自動起動する
	startNow := askYes(in, stdout, "Start the gateway now? (`gateway up` — idempotent)", true)
	return sum, startNow, nil
}

// PrintSummary は完了サマリの対話表示(cmd層の非対話モードでも使う)。
func PrintSummary(sum *Summary, w io.Writer) {
	fmt.Fprintln(w, "──────────────────────────────────────────────")
	fmt.Fprintf(w, "Setup complete.\n  runtime dir : %s\n  provider    : %s (gateway key fp %s, encrypted)\n  alias       : %s → %s\n",
		sum.RuntimeDir, sum.Provider, sum.GatewayKeyFP, sum.Alias, sum.Model)
	fmt.Fprintf(w, "  base url    : %s\n", sum.BaseURL)
	note := modelNote(sum.Alias, sum.Model)
	if note != "" {
		fmt.Fprintf(w, "    %s\n", note)
	}
	if sum.PiRegistered {
		fmt.Fprintf(w, "  pi          : registered (%d alias models written to models.json)\n", sum.PiModels)
	} else {
		fmt.Fprintln(w, "  pi          : skipped (register anytime with menu item 4)")
	}
	fmt.Fprintf(w, "  startup     : manual launcher at %s\n", sum.Port)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Gateway starts with: run the launcher, or `gateway up`.")
	fmt.Fprintln(w, "In pi: /model -> your alias.  If pi was already open, restart pi to pick up the new provider.")
}

func modelNote(alias, model string) string {
	return fmt.Sprintf("%s -> %s", alias, model)
}

// --- 設問ヘルパ ------------------------------------------------------------

func askProvider(in *bufio.Reader, out io.Writer) (Provider, bool) {
	if p, found := DetectEnv(os.Getenv); found {
		fmt.Fprintf(out, "\n[1/5] Found %s in your environment.\n", p.EnvVar)
		if askYes(in, out, fmt.Sprintf("Use provider %s?", p.ID), true) {
			return p, true
		}
	}
	fmt.Fprintln(out, "\n[1/5] Which provider do you want to use?")
	n := 0
	idx := []Provider(nil)
	for _, p := range Catalog() {
		if p.Disabled {
			continue
		}
		n++
		fmt.Fprintf(out, "  %d) %-12s %s\n", n, p.ID, p.Title)
		idx = append(idx, p)
	}
	for {
		line := askLine(in, out, "Choose [1]", "1")
		if line == "" {
			return idx[0], true
		}
		if v, ok := pickNumber(line, len(idx)); ok {
			return idx[v-1], true
		}
		if id, ok := Look(line); ok && !id.Disabled {
			return id, true
		}
		fmt.Fprintf(out, "  choose 1-%d or a provider id (%s)\n", len(idx), strings.Join(EnabledIDs(), ", "))
	}
}

func askAPIKey(in *bufio.Reader, out io.Writer, prov Provider) (string, bool) {
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "[2/5] Provide the API key for %s (%s).\n", prov.ID, prov.Title)
	fmt.Fprintf(out, "  Paste the key, or type `env:%s`, `file:PATH`, or `-` then Enter to read stdin.\n", prov.EnvVar)
	if prov.Note != "" {
		fmt.Fprintf(out, "  note: %s\n", prov.Note)
	}
	for {
		line, ok := readLine(in, out, "> ")
		if !ok {
			return "", false
		}
		if _, err := ResolveAPIKeySource(line, in); err == nil {
			return line, true
		} else {
			fmt.Fprintf(out, "  %v\n", err)
			if line == "" {
				return "", false
			}
		}
	}
}

func askModelAlias(in *bufio.Reader, out io.Writer, prov Provider) (model, alias string, ok bool) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "[3/5] Which model, and under which alias?")
	if prov.Note != "" {
		fmt.Fprintf(out, "  note: %s\n", prov.Note)
	}
	model, ok = readLineDefault(in, out, fmt.Sprintf("  Model [%s]: ", prov.Suggest), prov.Suggest)
	if !ok {
		return "", "", false
	}
	alias, ok = readLineDefault(in, out, "  Alias  [pi-default]: ", "pi-default")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(model), strings.TrimSpace(alias), true
}

func askStartup(in *bufio.Reader, out io.Writer) (string, bool) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "[5/5] How should the gateway start?")
	fmt.Fprintln(out, "  1) Start manually with `gateway up` (recommended for this release)")
	fmt.Fprintln(out, "  2) Auto-start at login + dormant mode (lands in a later release)")
	for {
		line, ok := readLineDefault(in, out, "Choose [1]: ", "1")
		if !ok {
			return "", false
		}
		switch strings.TrimSpace(line) {
		case "1", "":
			return "manual", true
		case "2":
			return "resident", true
		default:
			fmt.Fprintln(out, "  choose 1 or 2")
		}
	}
}

func askYes(in *bufio.Reader, out io.Writer, prompt string, defY bool) bool {
	suffix := " [Y/n]"
	if !defY {
		suffix = " [y/N]"
	}
	for {
		line, ok := readLineDefault(in, out, prompt+suffix+": ", "")
		if !ok {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			return defY
		case "n", "no":
			return !defY
		default:
			fmt.Fprintln(out, "  answer y or n")
		}
	}
}

func askLine(in *bufio.Reader, out io.Writer, prompt, def string) string {
	line, _ := readLineDefault(in, out, prompt+": ", def)
	return strings.TrimSpace(line)
}

func pickNumber(s string, max int) (int, bool) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 1 || n > max {
		return 0, false
	}
	return n, true
}

func readLine(in *bufio.Reader, out io.Writer, prompt string) (string, bool) {
	fmt.Fprint(out, prompt)
	line, err := in.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		if errors.Is(err, io.EOF) {
			return "", false
		}
	}
	return strings.TrimRight(line, "\r\n"), true
}

func readLineDefault(in *bufio.Reader, out io.Writer, prompt, def string) (string, bool) {
	line, ok := readLine(in, out, prompt)
	if !ok {
		return "", false
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, true
	}
	return line, true
}

// --- メニュー(再実行、§2.2の初期形 1/2/4/q) ---------------------------------

// MenuLoop はセットアップ済み環境で裸 gateway / init を実行したときの対話ループ。
func MenuLoop(stdin io.Reader, stdout io.Writer) error {
	paths, _, _, err := runtimeFromEnv()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Managing your gateway. Type a number, or q to quit.")
	for {
		sum := currentSummary(paths)
		fmt.Fprintln(stdout, "")
		fmt.Fprintf(stdout, "  1) Add a provider\n")
		fmt.Fprintf(stdout, "  2) Replace a provider API key\n")
		fmt.Fprintf(stdout, "  4) Register pi (models.json) again\n")
		fmt.Fprintf(stdout, "  q) Quit\n")
		if !sum.Empty {
			fmt.Fprintf(stdout, "  current: %s / providers=%v / default=%s / running=%s\n",
				sum.Dir, sum.Providers, sum.DefaultModel, sum.Running)
		}
		in := bufio.NewReader(stdin)
		line, ok := readLineDefault(in, stdout, "> ", "q")
		if !ok {
			return nil
		}
		switch strings.TrimSpace(line) {
		case "1":
			if err := menuAddProvider(paths, in, stdout); err != nil {
				if errors.Is(err, ErrAborted) {
					continue
				}
				fmt.Fprintf(stdout, "error: %v\n", err)
			}
		case "2":
			if err := menuReplaceKey(paths, in, stdout); err != nil {
				if errors.Is(err, ErrAborted) {
					continue
				}
				fmt.Fprintf(stdout, "error: %v\n", err)
			}
		case "4":
			if err := menuPiRegister(paths, stdout); err != nil {
				fmt.Fprintf(stdout, "error: %v\n", err)
			}
		case "", "q", "quit", "exit":
			return nil
		default:
			fmt.Fprintln(stdout, "  unknown choice (1 / 2 / 4 / q)")
		}
	}
}

// MenuRuntime はメニュー/隠しCLIが前提とする環境(runtime paths + secrets + recipient)を公開版で。
// MenuRuntime は runtimeFromEnv のエクスポート版(cmd層の隠しCLIからも使う)。
func MenuRuntime() (setup.Paths, *secrets.Secrets, string, error) { return runtimeFromEnv() }

func runtimeFromEnv() (setup.Paths, *secrets.Secrets, string, error) {
	dir, err := setup.ResolveDir("")
	if err != nil {
		return setup.Paths{}, nil, "", err
	}
	paths := setup.Layout(dir)
	data, err := os.ReadFile(paths.Identity)
	if err != nil {
		return paths, nil, "", fmt.Errorf("identityが読めません(%s): %w — `gateway` で再セットアップしてください", paths.Identity, err)
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		return paths, nil, "", err
	}
	sec, err := secrets.Load(secrets.LoadOptions{EncryptedPath: paths.Secrets,
		Identity: strings.NewReader(id.String())})
	if err != nil {
		return paths, nil, "", fmt.Errorf("secretsの復号に失敗: %w", err)
	}
	recipients := sec.IdentityRecipients
	if len(recipients) == 0 {
		recipients = []string{id.Recipient().String()}
	}
	return paths, sec, id.Recipient().String(), nil
}

func menuAddProvider(paths setup.Paths, in *bufio.Reader, out io.Writer) error {
	prov, ok := askProvider(in, out)
	if !ok {
		return ErrAborted
	}
	spec, ok := askAPIKey(in, out, prov)
	if !ok {
		return ErrAborted
	}
	key, err := ResolveAPIKeySource(spec, in)
	if err != nil {
		return err
	}
	model, alias, ok := askModelAlias(in, out, prov)
	if !ok {
		return ErrAborted
	}
	if err := ops.UpsertProvider(paths.Config, prov.ID, prov.BaseURL); err != nil {
		return err
	}
	sec, recipients, err := secretsForPaths(paths)
	if err != nil {
		return err
	}
	if sec.APIKeys == nil {
		sec.APIKeys = map[string]string{}
	}
	sec.APIKeys[prov.ID] = key
	if err := secrets.Save(paths.Secrets, sec, recipients); err != nil {
		return err
	}
	isDefault := !hasDefaultModel(paths.Config)
	if err := ops.UpsertAlias(paths.Config, ops.AliasSpec{Alias: alias, Provider: prov.ID,
		Model: model, Default: isDefault}); err != nil {
		return err
	}
	port := menuPort(paths)
	notifyReloadIfRunning(out, paths, port)
	fmt.Fprintf(out, "added: %s → %s / %s%s\n", alias, prov.ID, model, defaultMark(isDefault))
	return nil
}

func menuReplaceKey(paths setup.Paths, in *bufio.Reader, out io.Writer) error {
	provs := currentProviders(paths)
	if len(provs) == 0 {
		return errors.New("configにプロバイダーがありません")
	}
	fmt.Fprintln(out, "")
	for i, p := range provs {
		fmt.Fprintf(out, "  %d) %s\n", i+1, p)
	}
	line, ok := readLineDefault(in, out, fmt.Sprintf("Which provider [1-%d]: ", len(provs)), "1")
	if !ok {
		return ErrAborted
	}
	n, okN := pickNumber(strings.TrimSpace(line), len(provs))
	if !okN {
		return fmt.Errorf("番号で選んでください: %v", provs)
	}
	target := provs[n-1]
	prov, exists := Look(target)
	if !exists {
		// 手動追加した未対応provider → 素の文字列で扱う
		prov = Provider{ID: target}
	}
	spec, ok := askAPIKey(in, out, prov)
	if !ok {
		return ErrAborted
	}
	key, err := ResolveAPIKeySource(spec, in)
	if err != nil {
		return err
	}
	sec, recipients, err := secretsForPaths(paths)
	if err != nil {
		return err
	}
	if sec.APIKeys == nil {
		sec.APIKeys = map[string]string{}
	}
	sec.APIKeys[target] = key
	if err := secrets.Save(paths.Secrets, sec, recipients); err != nil {
		return err
	}
	notifyReloadIfRunning(out, paths, menuPort(paths))
	fmt.Fprintf(out, "key replaced: %s (fp %s). No pi-side change needed.\n", target, fp(key))
	return nil
}

func menuPiRegister(paths setup.Paths, out io.Writer) error {
	sec, _, err := secretsForPaths(paths)
	if err != nil {
		return err
	}
	aliases, err := aliasesForPi(paths.Config, "")
	if err != nil {
		return err
	}
	port := menuPort(paths)
	gwKeys := sec.AllGatewayKeys()
	res, err := ops.PiInstall(ops.PiInstallOptions{
		ModelsPath: piModelsPath(),
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%s/v1", port),
		Aliases:    aliases,
		GatewayKey: firstOr(gwKeys, ""),
		HasGatewayKey: func(k string) bool {
			for _, kk := range gwKeys {
				if kk == k {
					return true
				}
			}
			return false
		},
		// GatewayKeyが無い場合のfallback: ここで生成してsecrets保存まで行う
		NewGatewayKey: func() (string, error) {
			k, err := GenerateGatewayKey()
			if err != nil {
				return "", err
			}
			sec.GatewayKeys = appendUnique(sec.GatewayKeys, k)
			if r := sec.IdentityRecipients; len(r) > 0 {
				if err := secrets.Save(paths.Secrets, sec, r); err != nil {
					return "", err
				}
			}
			return k, nil
		},
	})
	if err != nil {
		return err
	}
	keep := "kept existing key"
	if !res.ReusedKey {
		keep = "generated a new key (also added to secrets)"
	}
	if res.BackupPath != "" {
		fmt.Fprintf(out, "backup: %s\n", res.BackupPath)
	}
	fmt.Fprintf(out, "pi models.json updated: %d alias models, gateway key %s.\n", res.ModelsCount, keep)
	fmt.Fprintln(out, "If pi was already open, restart pi (or run /reload in pi where available).")
	return nil
}

// secretsForPaths はメニュー系の共通ローダ(identity file 由来)。
func secretsForPaths(paths setup.Paths) (*secrets.Secrets, []string, error) {
	_, sec, recipient, err := runtimeFromEnv()
	if err != nil {
		return nil, nil, err
	}
	recs := sec.IdentityRecipients
	if len(recs) == 0 {
		recs = []string{recipient}
	}
	return sec, recs, nil
}

func notifyReloadIfRunning(out io.Writer, paths setup.Paths, port string) {
	if !httpAlive(port) {
		fmt.Fprintln(out, "note: gateway is not running; the change applies on next start.")
		return
	}
	sec, err := loadSecretsQuiet(paths)
	if err != nil {
		fmt.Fprintf(out, "note: could not notify reload (%v). Restart the gateway to apply.\n", err)
		return
	}
	keys := sec.AllGatewayKeys()
	if len(keys) == 0 {
		return
	}
	if err := NotifyReload("", keys[0]); err != nil {
		fmt.Fprintf(out, "note: reload通知に失敗したため、再起動で反映されます (%v)\n", err)
		return
	}
	fmt.Fprintln(out, "change notified to the running gateway (reload).")
}

func currentProviders(paths setup.Paths) []string {
	provs, err := configProviders(paths.Config)
	if err != nil {
		return nil
	}
	return provs
}

// currentSummary はメニュー上部に表示する現在状態(軽量・非秘匿)。
type menuState struct {
	Empty        bool
	Dir          string
	Providers    string
	DefaultModel string
	Running      string
}

func currentSummary(paths setup.Paths) menuState {
	st := menuState{Dir: paths.Dir}
	if _, err := os.Stat(paths.Config); err != nil {
		st.Empty = true
		return st
	}
	st.Providers = strings.Join(currentProviders(paths), ",")
	if f, err := readYAMLAliases(paths.Config); err == nil {
		st.DefaultModel = f.DefaultModel
	}
	if httpAlive(menuPort(paths)) {
		st.Running = "yes"
	} else {
		st.Running = "no (`gateway up` で起動)"
	}
	return st
}

// MenuPort はメニューが使うポート解決(env PORT > setup.json > 8080)のエクスポート版。
func MenuPort(paths setup.Paths) string { return menuPort(paths) }

func menuPort(paths setup.Paths) string {
	if v := os.Getenv("PORT"); v != "" {
		return v
	}
	if st, err := setup.LoadState(paths); err == nil && st != nil && st.Port != 0 {
		return fmt.Sprint(st.Port)
	}
	return "8080"
}

// httpAlive は /healthz への軽い生存確認(認証不要)。
func httpAlive(port string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return list[0]
	}
	return def
}

func defaultMark(isDefault bool) string {
	if isDefault {
		return " (default)"
	}
	return ""
}

func loadSecretsQuiet(paths setup.Paths) (*secrets.Secrets, error) {
	sec, _, err := secretsForPaths(paths)
	return sec, err
}
