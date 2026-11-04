//go:build !windows

// Windows以外のdoctor検査ヘルパ(10. codepage / 7. autostart実体検査の非実装口)。
package ops

// consoleOutputCodePage は非Windowsでは検査対象外(doctor側でskip表示にする)。
func consoleOutputCodePage() (int, bool) { return 0, false }

// autostartProbe は非Windowsでは未実装(第3バッチのLaunchAgent/systemd --user導入時)。
func autostartProbe() (string, bool) { return "", false }
