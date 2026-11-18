//go:build windows

// Windows固有のdoctor検査ヘルパ(10. codepage)。
package ops

import (
	"golang.org/x/sys/windows"
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
