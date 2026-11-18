// serve コマンドのフラグ(design-batch3.md §2.1/§3)。
//
// gateway serve --winhide: Windows専用。起動直後に自コンソールを非表示にする。
// Task Schedulerのログオントリガーから起動した場合のコンソール点滅を防ぐ。
// 手動のフォアグラウンドserveでは付けない(通常表示)。
//
// gateway serve --dormant / --no-dormant: ドーマント起動の明示的な上書き。
// 既定は setup.json startup=="resident"、env GATEWAY_DORMANT で上書き。フラグが最優先。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/awnumar/memguard"
)

// serveCmd は serve の引数解析と起動(run)。メインのrouteNewから呼ばれる。
func serveCmd(rest []string) error {
	defer memguard.Purge()
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // 出力は独自に制御する(flagのUsageダンプを避ける)
	winhide := fs.Bool("winhide", false, "起動時に自コンソールを非表示化(Windows専用。Task Scheduler登録用)")
	dormant := fs.Bool("dormant", false, "ドーマント起動を強制(setup.json/startup・envより優先)")
	noDormant := fs.Bool("no-dormant", false, "ドーマントを無効化して起動(通常ロード)")
	if err := fs.Parse(rest); err != nil {
		if err == flag.ErrHelp {
			printServeUsage(os.Stdout)
			return nil
		}
		printServeUsage(os.Stdout)
		return fmt.Errorf("serveの引数解析に失敗しました: %w", err)
	}
	if *dormant && *noDormant {
		return fmt.Errorf("--dormant と --no-dormant は同時に指定できません")
	}
	switch {
	case *dormant:
		serveDormantOverride = 1
	case *noDormant:
		serveDormantOverride = -1
	}
	if *winhide {
		hideConsoleWindow()
	}
	return run(false)
}

func printServeUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway serve [flags]

Run the gateway in the foreground (hidden/legacy entry; the OS registration runs this directly).
Configuration is self-discovered from the runtime dir (config/secrets/identity, PORT/BIND via
env > setup.json > launcher > 8080).

flags:
  -winhide      hide this console window right after start (Windows only; used by the Task Scheduler registration)
  -dormant      force dormant start (no keys loaded until the first request)
  -no-dormant   force a normal (non-dormant) start
`)
}
