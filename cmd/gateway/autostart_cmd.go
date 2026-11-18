// gateway autostart — ログイン常駐のための OS 直接登録(`gateway serve` 1本)。
//
// 使い方(pi coding agentの利用を想定。メニュー「起動方式変更」と同一コア):
//
//	gateway autostart install   OSにserveを登録(systemd --user / LaunchAgent / Task Scheduler)
//	gateway autostart uninstall 登録を除去(ランチャ・鍵・configは触らない)
//	gateway autostart status    登録状態・PATH組込状態・startup設定を表示
//
// 登録されるコマンドラインはOS共通で `<runtime>/bin/gateway(.exe) serve` の絶対パス1本のみ
// (PR#1 プロセス契約: PORT/BIND/identityはserve自身がsetup.Discoverで解決する)。
// WindowsはTask Scheduler XML(LogonTrigger + --winhide)、Linuxはsystemd --user unit、
// macOSはLaunchAgent plist。--printは登録定義をstdoutへ出力するだけのdry-run(E2E/CI用)。
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

func autostartCmd(rest []string, stdout io.Writer) error {
	flags, err := extractAutostartFlags(rest)
	if err != nil {
		if errors.Is(err, errHelp) {
			printAutostartUsage(stdout)
			return nil
		}
		printAutostartUsage(stdout)
		return err
	}
	if flags.action == "" {
		printAutostartUsage(stdout)
		return fmt.Errorf("autostart のサブコマンド(install|uninstall|status)を指定してください")
	}
	dir, err := setup.ResolveDir(flags.runtimeDir)
	if err != nil {
		return err
	}
	paths := setup.Layout(dir)
	switch flags.action {
	case "install":
		res, err := ops.AutostartInstall(paths, ops.AutostartOptions{Print: flags.print, Path: flags.path})
		if err != nil {
			return err
		}
		return printAutostartResult(stdout, res, "install", flags.print)
	case "uninstall":
		res, err := ops.AutostartUninstall(paths, ops.AutostartOptions{Path: flags.path})
		if err != nil {
			return err
		}
		return printAutostartResult(stdout, res, "uninstall", flags.print)
	case "status":
		st, err := ops.AutostartStatusInfo(paths)
		if err != nil {
			return err
		}
		printAutostartStatus(stdout, st)
		return nil
	default:
		printAutostartUsage(stdout)
		return fmt.Errorf("未知のautostartサブコマンド %q(install|uninstall|status)", flags.action)
	}
}

// autostartFlags はautostart CLIのオプション。
type autostartFlags struct {
	action     string // install | uninstall | status
	runtimeDir string // --runtime-dir(空ならsetup.ResolveDirの既定)
	print      bool   // --print(dry-run: 登録定義の表示のみ)
	path       bool   // --path(Windowsのみ: ユーザーPATHへの組込/除去)
}

// extractAutostartFlags は位置引数とフラグの前後どちらでも受け取る簡易解析
// (標準flagは最初の非フラグ引数で解析停止するため、keys/up系と同じ自前方式)。
func extractAutostartFlags(rest []string) (*autostartFlags, error) {
	f := &autostartFlags{}
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		switch {
		case tok == "-h" || tok == "-help" || tok == "--help":
			return nil, errHelp
		case tok == "--print" || tok == "-print":
			f.print = true
		case tok == "--path" || tok == "-path":
			f.path = true
		case tok == "--runtime-dir" || tok == "-runtime-dir":
			if i+1 >= len(rest) {
				return nil, fmt.Errorf("--runtime-dir の値がありません")
			}
			f.runtimeDir = rest[i+1]
			i++
		case strings.HasPrefix(tok, "--runtime-dir="):
			f.runtimeDir = strings.TrimPrefix(tok, "--runtime-dir=")
		case strings.HasPrefix(tok, "-runtime-dir="):
			f.runtimeDir = strings.TrimPrefix(tok, "-runtime-dir=")
		default:
			if f.action != "" {
				return nil, fmt.Errorf("未知の引数 %q(サブコマンドはinstall|uninstall|statusのいずれか1つ)", tok)
			}
			f.action = strings.TrimLeft(tok, "-")
		}
	}
	return f, nil
}

func printAutostartUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway autostart install | uninstall | status [flags]

Register 'gateway serve' directly with the OS so it runs at login (resident mode):
  install    create the registration: Task Scheduler (Windows, `+"`serve --winhide`"+`) /
             systemd --user unit (Linux) / LaunchAgent plist (macOS)
  uninstall  remove the registration (launcher/keys/config are untouched)
  status     show the registration state, user-PATH integration and startup mode

flags:
  --runtime-dir DIR   runtime dir to register (default: ~/.pi-gateway)
  --print             dry-run: print the registration definition only, no side effects (E2E/CI)
  --path              (Windows) add/remove <runtime-dir>\bin in the user PATH (HKCU Environment; never setx)
`)
}

// printAutostartResult はinstall/uninstallの結果表示。
// --print(stdout=登録定義本体。情報行はstderr)はE2E/CIのgrep対象を安定させるための分離。
func printAutostartResult(stdout io.Writer, res ops.AutostartResult, verb string, dryRun bool) error {
	if dryRun {
		fmt.Fprint(stdout, res.Definition)
		if res.Definition != "" && !strings.HasSuffix(res.Definition, "\n") {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(os.Stderr, "kind: %s\n", res.Kind)
		fmt.Fprintf(os.Stderr, "target: %s\n", res.Target)
		for _, n := range res.Notes {
			fmt.Fprintf(os.Stderr, "note: %s\n", n)
		}
		return nil
	}
	fmt.Fprintf(stdout, "autostart %s: %s %s\n", verb, res.Kind, res.Target)
	for _, n := range res.Notes {
		fmt.Fprintf(stdout, "note: %s\n", n)
	}
	return nil
}

// printAutostartStatus は `gateway autostart status` の本体表示。
// 「registered/not registered」のトークンはスクリプト判別の公共契約(E2Eで使用)。
func printAutostartStatus(stdout io.Writer, st ops.AutostartStatus) {
	state := "not registered"
	if st.Registered {
		state = "registered"
	}
	fmt.Fprintf(stdout, "autostart: %s (%s) — %s\n", state, st.Kind, st.Detail)
	if st.Registered && st.Command != "" {
		fmt.Fprintf(stdout, "  command: %s\n", st.Command)
	}
	if st.Legacy {
		fmt.Fprintf(stdout, "  legacy: 旧ランチャ(up.cmd等)参照の登録が残っています — `gateway autostart install` で置換できます\n")
	}
	switch st.PathState {
	case "added":
		fmt.Fprintf(stdout, "  PATH: added (%s)\n", st.PathEntry)
	case "not added":
		fmt.Fprintf(stdout, "  PATH: not added (%s — add with `gateway autostart install --path`)\n", st.PathEntry)
	default:
		// 非Windowsは表示しない
	}
	switch st.Startup {
	case "resident":
		fmt.Fprintf(stdout, "  startup: resident — serveはdormantで起動します(初回リクエストで鍵を復号。無効化: `gateway serve --no-dormant`)\n")
	case "manual":
		fmt.Fprintf(stdout, "  startup: manual — 常駐にするには `gateway autostart install`\n")
	default:
		fmt.Fprintf(stdout, "  startup: (not recorded) — dormantは環境変数 GATEWAY_DORMANT か `gateway serve --dormant` で制御\n")
	}
}
