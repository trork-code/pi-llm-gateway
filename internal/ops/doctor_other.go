//go:build !windows

// Windows以外のdoctor検査ヘルパ(10. codepageの非実装口)。
package ops

// consoleOutputCodePage は非Windowsでは検査対象外(doctor側でskip表示にする)。
func consoleOutputCodePage() (int, bool) { return 0, false }
