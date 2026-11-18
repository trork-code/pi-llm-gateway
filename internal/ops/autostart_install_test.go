package ops

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// --print(dry-run)はOSへの副作用を一切出さない(§2)。Windows上で検証可能な範囲を固定する。

func TestAutostartInstallPrintNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	paths := setup.Layout(dir)
	// binを置かない(--printではSelfCopyもしない)
	res, err := AutostartInstall(paths, AutostartOptions{Print: true})
	if err != nil {
		t.Fatalf("AutostartInstall(print) error: %v", err)
	}
	if !strings.Contains(res.Definition, "serve") {
		t.Errorf("定義にserveが無い:\n%s", res.Definition)
	}
	switch runtime.GOOS {
	case "windows":
		if !strings.Contains(res.Definition, "--winhide") || !strings.Contains(res.Definition, "<LogonTrigger>") {
			t.Errorf("Windows定義の要素が足りない:\n%s", res.Definition)
		}
	case "linux":
		if !strings.Contains(res.Definition, "Restart=on-failure") {
			t.Errorf("systemd定義の要素が足りない:\n%s", res.Definition)
		}
		for _, n := range res.Notes {
			if strings.Contains(n, "binがありません") {
				t.Logf("note: %s", n)
			}
		}
	}
	// 副作用チェック: bin dirが作られない・unit/plistが書かれない
	if _, err := os.Stat(paths.Bin); !os.IsNotExist(err) {
		t.Errorf("--printでbinが配置された(副作用)")
	}
	if _, err := os.Stat(paths.Dir + "/bin"); os.IsNotExist(err) {
		// ok: binディレクトリ自体も作らない
	} else if entries, _ := os.ReadDir(filepath.Join(paths.Dir, "bin")); len(entries) == 0 {
		t.Logf("bin dirは空(許容)")
	} else {
		t.Errorf("--printでbinのコピーが起きた(副作用)")
	}
}

func TestAutostartStatusUnregistered(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("未対応OS %s", runtime.GOOS)
	}
	dir := t.TempDir()
	paths := setup.Layout(dir)
	st, err := AutostartStatusInfo(paths)
	if err != nil {
		t.Fatalf("AutostartStatusInfo error: %v", err)
	}
	if st.Name != AutostartName {
		t.Errorf("Name = %q", st.Name)
	}
	// 空のruntime dirでは登録は無い
	if st.Registered {
		t.Errorf("空runtime dirでRegistered=true: %+v", st)
	}
	if runtime.GOOS == "windows" {
		if st.PathState == "" {
			t.Errorf("WindowsはPATH状態を返すべき")
		}
	} else if st.PathState != PATHNotApplicable {
		t.Errorf("非WindowsのPATH状態 = %q(N/A期待)", st.PathState)
	}
}

func TestStartupResident(t *testing.T) {
	dir := t.TempDir()
	paths := setup.Layout(dir)
	// setup.json 無し(手動環境): 何も書かない
	if note := startupResident(paths); note != "" {
		t.Errorf("setup.json無しでnote非空: %q", note)
	}
	if _, err := os.Stat(paths.SetupJSON); !os.IsNotExist(err) {
		t.Errorf("setup.jsonが勝手に作られた(副作用)")
	}
	// setup.json あり: startupをresidentへ更新
	st := &setup.State{Version: 1, Port: 18080, Startup: "manual", GatewayVersion: "test"}
	if err := setup.SaveState(paths, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	note := startupResident(paths)
	if note == "" {
		t.Errorf("manual→resident更新のnoteが空")
	}
	st2, err := setup.LoadState(paths)
	if err != nil || st2 == nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st2.Startup != "resident" {
		t.Errorf("Startup = %q", st2.Startup)
	}
	// 既にresidentならnote無し(idempotent)
	if again := startupResident(paths); again != "" {
		t.Errorf("2回目でnote非空: %q", again)
	}
}

func TestStatusKindPerOS(t *testing.T) {
	got := statusKind()
	switch runtime.GOOS {
	case "windows":
		if got != "schtasks" {
			t.Errorf("schtasks期待: %q", got)
		}
	case "linux":
		if got != "systemd" {
			t.Errorf("systemd期待: %q", got)
		}
	case "darwin":
		if got != "launchagent" {
			t.Errorf("launchagent期待: %q", got)
		}
	}
}

func TestWriteUTF16LEBOM(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "utf16-*.xml")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	defer f.Close()
	if err := writeUTF16LEBOM(f, "pi-gateway serve \u00e9\U0001F4A9"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(f.Name())
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xFE {
		t.Fatalf("BOM無し: % x", data)
	}
	// 復号照合(LEのuint16列を文字列へ)
	units := make([]uint16, (len(data)-2)/2)
	for i := range units {
		units[i] = uint16(data[2+i*2]) | uint16(data[3+i*2])<<8
	}
	if got := string(utf16.Decode(units)); got != "pi-gateway serve \u00e9\U0001F4A9" {
		t.Errorf("復号不一致: %q", got)
	}
}

// userPathContainsはレジストリ読むためWindowsでのみ実測(読み取り専用)。
func TestUserPathContainsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windowsのみ")
	}
	dir := t.TempDir()
	added, detail, literal := userPathContains(filepath.Join(dir, "bin"))
	if added {
		t.Skipf("テンポラリ配下のbinが実際にPATH組込済みになった環境(特異): %s %s", detail, literal)
	}
	if literal == "" {
		t.Errorf("literal entryが空")
	}
}
