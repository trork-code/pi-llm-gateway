//go:build !windows

package main

// hideConsoleWindow はWindows専用(serve --winhide)。他OSでは何もしない。
func hideConsoleWindow() {}
