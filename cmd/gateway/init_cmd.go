package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/trork-code/pi-llm-gateway/internal/setup"
	"github.com/trork-code/pi-llm-gateway/internal/wizard"
)

// initCmd は公開CLI `gateway init`(および裸 gateway)の本体。
// design-distribution.md §2: 対話 / 非対話(--yes) / メニュー(済み環境)の3形態。
func initCmd(rest []string, stdout io.Writer) error {
	fl := initFlags{}
	if err := parseInitFlags(rest, &fl); err != nil {
		printInitUsage(stdout)
		return err
	}
	if fl.help {
		printInitUsage(stdout)
		return nil
	}

	dir, err := setup.ResolveDir(fl.runtimeDir)
	if err != nil {
		return err
	}
	paths := setup.Layout(dir)
	st, err := setup.LoadState(paths)
	if err != nil {
		return err
	}
	complete := setup.IsComplete(paths, st)

	app := wizard.Options{
		RuntimeDir:        fl.runtimeDir,
		Provider:          fl.provider,
		APIKeySpec:        fl.apiKey,
		Model:             fl.model,
		Alias:             fl.alias,
		Pi:                fl.pi == nil || *fl.pi,
		Startup:           fl.startup,
		Port:              fl.port,
		Bind:              fl.bind,
		Yes:               fl.yes,
		PrintJSON:         fl.printJSON,
		Quiet:             fl.quiet,
		ForcePiKeyReplace: fl.forcePiKeyReplace,
		Stdin:             os.Stdin,
	}

	if fl.yes {
		return runNonInteractiveInit(app, paths, fl.noStart, stdout)
	}
	if !complete && !fl.force {
		sum, startNow, err := wizard.RunInteractive(app, os.Stdin, stdout)
		if err != nil {
			if errors.Is(err, wizard.ErrAborted) {
				return nil
			}
			return err
		}
		if startNow && sum != nil {
			startGatewayNow(paths, sum.Port, stdout)
		}
		return nil
	}
	// セットアップ済み → メニュー(対話前提)
	if !isTTY(os.Stdin) {
		fmt.Fprintln(stdout, "Gateway is already set up at "+dir+".")
		fmt.Fprintln(stdout, "For non-interactive changes: gateway init --provider <p> --api-key env:<VAR> --yes")
		consoleHint(stdout)
		return nil
	}
	return wizard.MenuLoop(os.Stdin, stdout)
}

// runNonInteractiveInit は --yes モード。対話なしで完走し、自動起動も行う。
func runNonInteractiveInit(app wizard.Options, paths setup.Paths, noStart bool, stdout io.Writer) error {
	if app.Provider == "" || app.APIKeySpec == "" {
		printInitUsage(stdout)
		return errors.New("--provider と --api-key は非対話モード(--yes)で必須です")
	}
	sum, err := wizard.RunSetup(app, stdout)
	if err != nil {
		return err
	}
	if !app.Quiet {
		wizard.PrintSummary(sum, stdout)
	}
	if !noStart {
		startGatewayNow(paths, sum.Port, stdout)
	}
	return nil
}

// startGatewayNow はセットアップ直後に冪等 up で常駐を開始する。
func startGatewayNow(paths setup.Paths, port string, stdout io.Writer) {
	saved := map[string]string{}
	for k, v := range map[string]string{
		"PORT":              port,
		"BIND":              "127.0.0.1",
		"CONFIG_FILE":       paths.Config,
		"SECRETS_FILE":      paths.Secrets,
		"AGE_IDENTITY_FILE": paths.Identity,
	} {
		saved[k] = os.Getenv(k)
		_ = os.Setenv(k, v)
	}
	defer func() {
		for k, v := range saved {
			_ = os.Setenv(k, v)
		}
	}()
	fmt.Fprintf(stdout, "starting the gateway at http://127.0.0.1:%s ...\n", port)
	if err := serviceUp(slog.Default(), "http://127.0.0.1:"+port, paths.Log, stdout); err != nil {
		fmt.Fprintf(stdout, "note: auto-start failed (%v). Run the launcher (%s) later.\n", err, paths.Launcher)
	}
}

// isTTY は stdin が端末か(パイプ・リダイレクトなら対話を出さない)。
func isTTY(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func consoleHint(stdout io.Writer) {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(stdout, "Interactive menu requires a terminal. Run `gateway` directly (not piped).")
		return
	}
	fmt.Fprintln(stdout, "Interactive menu requires a terminal. Run `gateway` directly (not piped).")
}

// --- フラグ解析(keys と同じ自前走査: 位置引数なし・フラグのみ) --------------

type initFlags struct {
	provider          string
	apiKey            string
	model             string
	alias             string
	pi                *bool
	startup           string
	port              string
	bind              string
	runtimeDir        string
	yes               bool
	printJSON         bool
	quiet             bool
	help              bool
	force             bool
	forcePiKeyReplace bool
	noStart           bool
}

func parseInitFlags(rest []string, fl *initFlags) error {
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		name, value, hasValue := splitFlag(tok)
		switch name {
		case "-h", "--h", "-help", "--help":
			fl.help = true
		case "-provider", "--provider":
			fl.provider = consumeValue(rest, &i, hasValue, value)
		case "-api-key", "--api-key":
			fl.apiKey = consumeValue(rest, &i, hasValue, value)
		case "-model", "--model":
			fl.model = consumeValue(rest, &i, hasValue, value)
		case "-alias", "--alias":
			fl.alias = consumeValue(rest, &i, hasValue, value)
		case "-startup", "--startup":
			fl.startup = consumeValue(rest, &i, hasValue, value)
		case "-port", "--port":
			fl.port = consumeValue(rest, &i, hasValue, value)
		case "-bind", "--bind":
			fl.bind = consumeValue(rest, &i, hasValue, value)
		case "-runtime-dir", "--runtime-dir":
			fl.runtimeDir = consumeValue(rest, &i, hasValue, value)
		case "-pi", "--pi":
			fl.pi = boolPtr(true)
		case "-no-pi", "--no-pi":
			fl.pi = boolPtr(false)
		case "-yes", "--yes", "-y":
			fl.yes = true
		case "-print-json", "--print-json":
			fl.printJSON = true
		case "-quiet", "--quiet":
			fl.quiet = true
		case "-force", "--force":
			fl.force = true
		case "-pi-key-replace", "--pi-key-replace":
			fl.forcePiKeyReplace = true
		case "-no-start", "--no-start":
			fl.noStart = true
		default:
			if tok == "--" {
				continue
			}
			if strings.HasPrefix(tok, "-") {
				return fmt.Errorf("未知のフラグ %q", tok)
			}
			return fmt.Errorf("予期しない引数 %q (init はフラグのみを受け付けます)", tok)
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

func splitFlag(tok string) (name, value string, hasValue bool) {
	if !strings.HasPrefix(tok, "-") {
		return tok, "", false
	}
	if i := strings.Index(tok, "="); i > 0 {
		return tok[:i], tok[i+1:], true
	}
	return tok, "", false
}

// consumeValue は --flag VALUE / --flag=VALUE / 省略("")の3形を吸収する。
// 値を次トークンから取り込む場合は走査位置を進める。
func consumeValue(rest []string, i *int, hasValue bool, value string) string {
	if hasValue {
		return value
	}
	if *i+1 < len(rest) && !strings.HasPrefix(rest[*i+1], "-") {
		*i++
		return rest[*i]
	}
	return ""
}

func printInitUsage(w io.Writer) {
	fmt.Fprintf(w, "usage: gateway init [flags]\n\nInteractive setup (first run) and management menu (afterwards).\nBare `gateway` behaves the same (with prompts).\n\nflags:\n")
	fmt.Fprintf(w, "  --provider NAME       one of: %s\n", strings.Join(wizard.EnabledIDs(), " / "))
	fmt.Fprint(w, `  --api-key SOURCE      value | env:NAME | file:PATH | - (stdin)
  --model M             upstream model name (default: provider suggestion)
  --alias A             pi model id (default: pi-default)
  --pi / --no-pi        register into ~/.pi/agent/models.json (default: register)
  --startup MODE        manual (this release) | resident (login auto-start, later release)
  --port N              runtime port (default: 18080)
  --bind ADDR           listen address (default: 127.0.0.1)
  --runtime-dir DIR     runtime dir override (default: ~/.pi-gateway)
  --yes                 non-interactive mode; --provider/--api-key required
  --print-json          machine-readable summary (for installers/E2E)
  --quiet               minimal output
  --pi-key-replace      (advanced) replace a pi gateway.apiKey absent from secrets
  --no-start            skip auto-start after setup
`)
}
