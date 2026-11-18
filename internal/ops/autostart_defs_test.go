package ops

import (
	"runtime"
	"strings"
	"testing"
)

func TestSystemdUnitDef(t *testing.T) {
	out := systemdUnitDef("/users/me/go/bin/gateway")
	for _, want := range []string{
		"ExecStart=\"/users/me/go/bin/gateway\" serve",
		"Type=simple",
		"Restart=on-failure",
		"RestartSec=3",
		"WantedBy=default.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("systemd unit に %q が無い:\n%s", want, out)
		}
	}
	// 焼き込み禁止(env割当て/ランチャ参照/秘匿値)の観点
	if strings.Contains(out, "PORT=") || strings.Contains(out, "BIND=") || strings.Contains(out, "up.sh") || strings.Contains(out, "AGE_") {
		t.Errorf("systemd unit に焼き込みっぽい記述がある:\n%s", out)
	}
}

func TestLaunchAgentDef(t *testing.T) {
	out := launchAgentDef("/Users/me/go/bin/gateway", "/Users/me/.pi-gateway/gateway.log")
	for _, want := range []string{
		"<string>" + LaunchAgentLabel + "</string>",
		"<string>/Users/me/go/bin/gateway</string>",
		"<string>serve</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
		"<string>/Users/me/.pi-gateway/gateway.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("launchagent plist に %q が無い:\n%s", want, out)
		}
	}
}

func TestWindowsTaskDef(t *testing.T) {
	out := windowsTaskDef(`C:\Users\me\.pi-gateway\bin\gateway.exe`, windowsServeArgs, "DESKTOP\\me", "desc <x>")
	for _, want := range []string{
		"<LogonTrigger>",
		"<UserId>DESKTOP\\me</UserId>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		`<Command>` + `C:\Users\me\.pi-gateway\bin\gateway.exe` + `</Command>`,
		"<Arguments>serve --winhide</Arguments>",
		"ExecutionTimeLimit>PT0S",
		"AllowStartOnDemand>true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("schtasks XML に %q が無い:\n%s", want, out)
		}
	}
	if strings.Contains(out, "desc <x>") {
		t.Errorf("XMLエスケープ漏れ:\n%s", out)
	}
	// --winhideはWindows登録の必須要素(コンソール非表示)
	if !strings.Contains(out, windowsServeArgs) {
		t.Errorf("Windows登録に%sが無い", windowsServeArgs)
	}
}

func TestEscapeXMLText(t *testing.T) {
	if got := escapeXMLText("a<>&b"); got != "a&lt;&gt;&amp;b" {
		t.Errorf("escapeXMLText = %q", got)
	}
}

func TestUserPathEntryFor(t *testing.T) {
	// filepath.Relの区切り解釈はGOOS依存のため、期待値もGOOS別に検証する。
	// (本関数はwindows実行時のみ実用途を持つが、相対化ロジック自体はどのGOOSでも検証したい)
	type c struct{ home, dir, want string }
	var cases []c
	if runtime.GOOS == "windows" {
		cases = []c{
			{`C:\Users\me`, `C:\Users\me\.pi-gateway\bin`, `%USERPROFILE%\.pi-gateway\bin`},
			{`C:\Users\me`, `D:\custom\gwbin`, `D:\custom\gwbin`},
		}
	} else {
		cases = []c{
			{`/users/me`, `/users/me/.pi-gateway/bin`, `%USERPROFILE%\.pi-gateway\bin`},
			{`/users/me`, `/opt/gwbin`, `/opt/gwbin`},
		}
	}
	for _, tc := range cases {
		if got := userPathEntryFor(tc.home, tc.dir); got != tc.want {
			t.Errorf("userPathEntryFor(%q,%q) = %q, want %q", tc.home, tc.dir, got, tc.want)
		}
	}
	// home空は絶対パスをそのまま返す(共通)
	if got := userPathEntryFor("", `C:\x`); got != `C:\x` {
		t.Errorf("userPathEntryFor(空home) = %q", got)
	}
}

func TestUserPathCompute(t *testing.T) {
	home := `C:\Users\me`
	// add: 追加
	next, changed := userPathCompute("C:\\a;C:\\b", `%USERPROFILE%\.pi-gateway\bin`, home, true)
	if !changed || next != "C:\\a;C:\\b;%USERPROFILE%\\.pi-gateway\\bin" {
		t.Errorf("add = %q (%v)", next, changed)
	}
	// add: 末尾セミコロン・空起点
	next, changed = userPathCompute("C:\\a;", `%USERPROFILE%\.pi-gateway\bin`, home, true)
	if !changed || next != "C:\\a;%USERPROFILE%\\.pi-gateway\\bin" {
		t.Errorf("add after ; = %q (%v)", next, changed)
	}
	next, changed = userPathCompute("", `%USERPROFILE%\.pi-gateway\bin`, home, true)
	if !changed || next != `%USERPROFILE%\.pi-gateway\bin` {
		t.Errorf("add to empty = %q (%v)", next, changed)
	}
	// add: 重複(literal/実パス/末尾\\差/大文字差)
	dups := []string{
		`%USERPROFILE%\.pi-gateway\bin`,
		`C:\Users\me\.pi-gateway\bin`,
		`C:\Users\me\.pi-gateway\bin\`,
		`%userprofile%\.pi-gateway\bin`,
	}
	for _, raw := range dups {
		if _, ch := userPathCompute(raw, `%USERPROFILE%\.pi-gateway\bin`, home, true); ch {
			t.Errorf("重複skipされない: raw=%q", raw)
		}
	}
	// add: 既存値が完全保持される(書換え副作用なし)
	raw := `"C:\Program Files\foo with space";C:\b`
	if _, ch := userPathCompute(raw, `C:\x`, home, true); ch != true {
		t.Fatalf("add should change")
	}
	next, _ = userPathCompute(raw, `C:\x`, home, true)
	if next != raw+`;C:\x` {
		t.Errorf("既存値が書換えられた: %q", next)
	}
	// remove: 該当entryのみ除去(両形式)
	next, changed = userPathCompute(`C:\a;%USERPROFILE%\.pi-gateway\bin;C:\b`, `C:\Users\me\.pi-gateway\bin`, home, false)
	if !changed || next != "C:\\a;C:\\b" {
		t.Errorf("remove = %q (%v)", next, changed)
	}
	// remove: 無ければ不変
	if _, ch := userPathCompute("C:\\a", `C:\x`, home, false); ch {
		t.Errorf("無いentryのremoveで変化した")
	}
}
