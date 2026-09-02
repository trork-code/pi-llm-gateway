//go:build windows

package hardening

// DisableCoreDumps はWindowsではno-op。
// Windowsのクラッシュダンプ(Watson/ミニダンプ)はOS設定(レジストリ・グループポリシー)で
// 制御するため、ここでは何もしない。デプロイ時にOS側で無効化すること。
func DisableCoreDumps() error { return nil }
