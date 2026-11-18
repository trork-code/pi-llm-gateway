package setup

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// LauncherInput はランチャ生成に必要な情報。
type LauncherInput struct {
	Port string // 例: 18080
	Bind string // 例: 127.0.0.1
	Log  string // ログ出力先(絶対パス)
	// IdentityMode: file → AGE_IDENTITY_FILE を設定する。
	// env → value に指定された環境変数名から AGE_IDENTITY_<NAME> を渡す。
	// cmd → SECRETS_DECRYPT_CMD に value を渡す。
	IdentityMode  string
	IdentityValue string
}

// WriteLauncher は up.cmd / up.sh を生成する。
// バッチ3 B案(design-batch3.md §1.2)で新形式: **env 割当てを書かない**。
// serve が runtime dir(標準配置/setup.json)を発見するため(PORT/identity も不要)。
// ログイン起動はこのランチャを使わない(autostart が serve を直接登録: C案)。ランチャは手動起動の便宜物。
// 旧形式(env付き)の既存ランチャはそのまま残る(1 リリース互換。up.cmd からの `gateway up`
// はB案でfork廃止済みのため、旧形式での起動はもはやしない=再生成時に新形式へ移行する)。
func WriteLauncher(paths Paths, in LauncherInput) error {
	var content string
	switch runtime.GOOS {
	case "windows":
		content = windowsLauncher(paths, in)
	case "linux", "darwin":
		content = unixLauncher(paths, in)
	default:
		return fmt.Errorf("未対応のOS: %s", runtime.GOOS)
	}
	mode := os.FileMode(0o644)
	if runtime.GOOS != "windows" {
		mode = 0o755
	}
	return atomicWrite(paths.Launcher, []byte(content), mode)
}

func windowsLauncher(paths Paths, in LauncherInput) string {
	_ = in // B案: env割当てをやめる(serveが発見)。LauncherInput 引数は C案の登録生成器との互換のため残す
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("rem pi-gateway manual-start helper (legacy; autostart registers `gateway serve` directly).\r\n")
	b.WriteString("rem No env here -- the binary resolves the install from the runtime dir (PR#1).\r\n")
	b.WriteString("cd /d \"%~dp0\"\r\n")
	b.WriteString(fmt.Sprintf("start \"\" /b \"%s\" serve\r\n", paths.Bin))
	b.WriteString("timeout /t 1 >nul\r\n")
	b.WriteString(fmt.Sprintf("\"%s\" status\r\n", paths.Bin))
	return b.String()
}

func unixLauncher(paths Paths, in LauncherInput) string {
	_ = in
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# pi-gateway manual-start helper (legacy; autostart registers `gateway serve` directly).\n")
	b.WriteString("# No env here -- the binary resolves the install from the runtime dir (PR#1).\n")
	b.WriteString("cd \"$(dirname \"$0\")\"\n")
	b.WriteString(fmt.Sprintf("\"%s\" serve &\n", paths.Bin))
	b.WriteString("sleep 1\n")
	b.WriteString(fmt.Sprintf("\"%s\" status\n", paths.Bin))
	return b.String()
}

// cmdlineFor は SECRETS_DECRYPT_CMD の値をランチャに安全に焼き込む。
// cmd: 委譲の値はコマンドライン文字列としてそのまま引用符で括る。
func cmdlineFor(v string, windows bool) string {
	v = strings.TrimSpace(v)
	if !strings.ContainsAny(v, " \t") {
		return v
	}
	if windows {
		return "\"" + strings.ReplaceAll(v, "\"", "\\\"") + "\""
	}
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}

func qWindows(p string) string { return p } // 呼び出し側で既に \"...\" で括る
