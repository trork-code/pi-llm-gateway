//go:build windows

package main

import "golang.org/x/sys/windows"

// hideConsoleWindow は自プロセスのコンソールウィンドウを非表示にする(serve --winhide)。
// Task Schedulerのログオントリガーから起動した場合にコンソールが点滅するのを防ぐ
// (design-batch3.md §2.1)。コンソールを持たない起動(detached等)では何もしない。
func hideConsoleWindow() {
	var hwnd uintptr
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	if r, _, _ := kernel32.NewProc("GetConsoleWindow").Call(); r != 0 {
		hwnd = r
	}
	if hwnd == 0 {
		return
	}
	const SW_HIDE = 0
	_, _, _ = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow").Call(hwnd, SW_HIDE)
}
