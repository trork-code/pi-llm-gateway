//go:build windows

// Windows固有のdoctor検査ヘルパ(10. codepage / 7. autostart実体検査)。
package ops

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// consoleOutputCodePage はコンソール出力コードページ(kernel32.GetConsoleOutputCP)。
// gatewayは常にUTF-8で書き出すため、cp932等のコンソールでは日本語が文字化けする。
// 判定は表示注意のみに使い、動作には影響させない。
func consoleOutputCodePage() (int, bool) {
	if cp, err := windows.GetConsoleOutputCP(); err == nil && cp != 0 {
		return int(cp), true
	}
	return 0, false
}

// autostartProbe はHKCU Run value(pi-gateway)の実体検査。
// 第3バッチでautostart install/uninstallが入るまでの「現状の見え方」。
// 登録済みなら (説明, true)。
func autostartProbe() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("pi-gateway")
	if err != nil {
		return "", false
	}
	return `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\pi-gateway = ` + v, true
}
