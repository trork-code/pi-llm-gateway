//go:build windows

package ops

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows向けユーザーPATH(HKCU\Environment\Path)への組込/除去(design-batch3.md §4)。
//
// setx を使わない(既存値のトランケート防止)。registry APIで型保持のまま書替える。
// REG_EXPAND_SZはそのまま保ち %USERPROFILE% 形式を保存。REG_SZは環境変数を
// 展開しないため、実パスを書き込む(型は保持)。
// 書込み後 WM_SETTINGCHANGE(HWND_BROADCAST, "Environment") を送り、
// 新しく開くシェルから有効化する(既存のシェル/アプリの環境は変わらない)。

// userPathRead はHKCU Environment Path の現行値と型(SZ=1/EXPAND_SZ=2/MULTI_SZ=7)を返す。
// 値が未作成の新規プロファイルでは(raw="", EXPAND_SZ扱い)を返す(書込み時に作成)。
func userPathRead() (string, uint32, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err != nil {
		return "", 0, err
	}
	defer k.Close()
	val, typ, err := k.GetStringValue("Path")
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", registry.EXPAND_SZ, nil
		}
		if errors.Is(err, registry.ErrUnexpectedType) && typ == registry.MULTI_SZ {
			// MULTI_SZ の Path は再結合を壊さないよう自動更新しない
			return "", typ, nil
		}
		return "", typ, err
	}
	return val, typ, nil
}

// userPathWrite はPath値を型保持のまま書き込み、環境変数変更を通知する。
func userPathWrite(next string, typ uint32) error {
	if typ == registry.MULTI_SZ {
		return fmt.Errorf("Path が REG_MULTI_SZ のため自動更新できません(手動で追加してください)")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("HKCU\\Environment のオープンに失敗しました: %w", err)
	}
	defer k.Close()
	if typ == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", next)
	} else {
		err = k.SetStringValue("Path", next)
	}
	if err != nil {
		return fmt.Errorf("HKCU\\Environment\\Path の更新に失敗しました: %w", err)
	}
	// WM_SETTINGCHANGEの通知はadvisory(失敗してもPath自体は更新済み)
	_ = broadcastEnvChange()
	return nil
}

// userPathEntryForDefault はbinディレクトリ組込時の保存形式(%USERPROFILE%形式 or 絶対パス)を決める。
func userPathEntryForDefault(binDir string) string {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home = os.Getenv("HOME")
	}
	return userPathEntryFor(home, binDir)
}

// userPathContains はbinディレクトリがユーザーPATHに組込済みかを判定する(読み取りのみ)。
// 戻り値: (組込済み, 詳細, 組込時に使う保存形式のentry)。
func userPathContains(binDir string) (bool, string, string) {
	literal := userPathEntryForDefault(binDir)
	raw, _, err := userPathRead()
	if err != nil {
		return false, fmt.Sprintf("PATH読取に失敗(%v)", err), literal
	}
	home := os.Getenv("USERPROFILE")
	for _, e := range strings.Split(raw, ";") {
		if userPathMatches(e, literal, home) {
			return true, "", literal
		}
	}
	return false, "", literal
}

// userPathAdd はbinディレクトリをユーザーPATHへ追加する(重複ならskip)。
func userPathAdd(binDir string) (note string, changed bool, err error) {
	home := os.Getenv("USERPROFILE")
	literal := userPathEntryForDefault(binDir)
	raw, typ, rerr := userPathRead()
	if rerr != nil {
		return "", false, rerr
	}
	entry := literal
	if typ == registry.SZ {
		entry = expandPortableEntry(literal, home) // REG_SZは%VAR%を展開しないため実パスを書く
	}
	next, changed := userPathCompute(raw, entry, home, true)
	if !changed {
		return fmt.Sprintf("PATH: %s は既に組込済みです(skip)", entry), false, nil
	}
	if err := userPathWrite(next, typ); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("PATH: %s をHKCUユーザーPATHへ追加しました(新しく開くシェルから有効)", entry), true, nil
}

// userPathRemove はbinディレクトリをユーザーPATHから除去する(無ければ何もしない)。
func userPathRemove(binDir string) (note string, changed bool, err error) {
	home := os.Getenv("USERPROFILE")
	literal := userPathEntryForDefault(binDir)
	raw, typ, rerr := userPathRead()
	if rerr != nil {
		return "", false, rerr
	}
	next, changed := userPathCompute(raw, literal, home, false)
	if !changed {
		return fmt.Sprintf("PATH: %s は組込まれていません(何もしません)", literal), false, nil
	}
	if err := userPathWrite(next, typ); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("PATH: %s をHKCUユーザーPATHから除去しました(新しく開くシェルから有効)", literal), true, nil
}

// broadcastEnvChange は環境変数の変更をシステムへ通知する(SendMessageTimeout HWND_BROADCAST)。
func broadcastEnvChange() error {
	const (
		HWND_BROADCAST   uintptr = 0xFFFF
		WM_SETTINGCHANGE uintptr = 0x001A
		SMTO_ABORTIFHUNG uintptr = 0x0002
	)
	p := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	ptr, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	r1, _, _ := p.Call(HWND_BROADCAST, WM_SETTINGCHANGE, 0, uintptr(unsafe.Pointer(ptr)), SMTO_ABORTIFHUNG, 5000, 0)
	if r1 == 0 {
		return fmt.Errorf("WM_SETTINGCHANGE のブロードキャストに失敗しました")
	}
	return nil
}
