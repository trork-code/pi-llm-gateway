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
// ランチャは冪等(`gateway up` の性質)で、ログイン時自動起動(バッチ3)と
// 手動起動の両方で同じものを使う。
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
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("rem pi-gateway launcher (idempotent). Safe to run any time.\r\n")
	b.WriteString("setlocal\r\n")
	b.WriteString(fmt.Sprintf("set PORT=%s\r\n", in.Port))
	b.WriteString(fmt.Sprintf("set BIND=%s\r\n", in.Bind))
	switch in.IdentityMode {
	case "env":
		// AGE_IDENTITY を env:<NAME> 経由で渡す(値そのものは書かない)
		fmt.Fprintf(&b, "set AGE_IDENTITY=%%%s%%\r\n", in.IdentityValue)
	case "cmd":
		fmt.Fprintf(&b, "set SECRETS_DECRYPT_CMD=%s\r\n", cmdlineFor(in.IdentityValue, true))
	default: // file
		fmt.Fprintf(&b, "set AGE_IDENTITY_FILE=%s\r\n", qWindows(paths.Identity))
	}
	fmt.Fprintf(&b, "set CONFIG_FILE=%s\r\n", qWindows(paths.Config))
	fmt.Fprintf(&b, "set SECRETS_FILE=%s\r\n", qWindows(paths.Secrets))
	fmt.Fprintf(&b, "cd /d \"%s\"\r\n", paths.Dir)
	fmt.Fprintf(&b, "\"%s\" up -log \"%s\"\r\n", qWindows(paths.Bin), in.Log)
	fmt.Fprintf(&b, "\"%s\" status -addr http://127.0.0.1:%s\r\n", qWindows(paths.Bin), in.Port)
	b.WriteString("endlocal\r\n")
	return b.String()
}

func unixLauncher(paths Paths, in LauncherInput) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# pi-gateway launcher (idempotent). Safe to run any time.\n")
	b.WriteString(fmt.Sprintf("PORT=%s\n", in.Port))
	b.WriteString(fmt.Sprintf("BIND=%s\n", in.Bind))
	b.WriteString("export PORT BIND\n")
	switch in.IdentityMode {
	case "env":
		// AGE_IDENTITY を env:<NAME> 経由で渡す(値そのものは書かない)
		fmt.Fprintf(&b, "AGE_IDENTITY=\"${%s}\"\n", in.IdentityValue)
		b.WriteString("export AGE_IDENTITY\n")
	case "cmd":
		fmt.Fprintf(&b, "SECRETS_DECRYPT_CMD=%s\n", cmdlineFor(in.IdentityValue, false))
		b.WriteString("export SECRETS_DECRYPT_CMD\n")
	default: // file
		fmt.Fprintf(&b, "AGE_IDENTITY_FILE=\"%s\"\n", paths.Identity)
		b.WriteString("export AGE_IDENTITY_FILE\n")
	}
	fmt.Fprintf(&b, "CONFIG_FILE=\"%s\"\n", paths.Config)
	fmt.Fprintf(&b, "SECRETS_FILE=\"%s\"\n", paths.Secrets)
	b.WriteString("export CONFIG_FILE SECRETS_FILE\n")
	fmt.Fprintf(&b, "\"%s\" up -log \"%s\"\n", paths.Bin, in.Log)
	fmt.Fprintf(&b, "\"%s\" status -addr \"http://127.0.0.1:${PORT}\"\n", paths.Bin)
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
