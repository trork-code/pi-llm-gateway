package ops

// ユーザーPATH(HKCU Environment\Path)へのentry追加/除去の純粋計算(副作用なし)。
// build tagを付けないため任意OSのunit testで検証できる(CIのlinux runnerでも走る)。
// レジストリ読書きとWM_SETTINGCHANGEはpath_windows.go(Windows専用)。

import (
	"path/filepath"
	"strings"
)

// userPathEntryFor は組込対象entryの保存形式を返す。
// 既定runtime dir(~/.pi-gateway/bin)は %USERPROFILE% 形式(ユーザー名変更に堅牢)。
// home配下でない/相対化できない場所は絶対パスをそのまま返す。Windows向け(§4)。
func userPathEntryFor(home, dir string) string {
	dir = strings.TrimSpace(dir)
	if home == "" || dir == "" {
		return dir
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return dir
	}
	rel = filepath.Clean(rel)
	rel = strings.ReplaceAll(rel, "/", `\`) // windows形式に統一(他OSテストでも一致するため)
	if rel == "." {
		return `%USERPROFILE%`
	}
	return `%USERPROFILE%\` + rel
}

// normalizePathEntry は重複比較用の正規化(前後空白除去+小文字+末尾バックスラッシュ除去)。
func normalizePathEntry(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	return strings.TrimRight(s, `\`)
}

// expandPortableEntry は %USERPROFILE% をhomeに置換して実パスにする(比較用。書込みはしない)。
// 大文字小文字の揺れ(%USERPROFILE% / %UserProfile%)も拾う。
func expandPortableEntry(s, home string) string {
	if home == "" {
		return s
	}
	out := s
	for {
		lower := strings.ToLower(out)
		i := strings.Index(lower, "%userprofile%")
		if i < 0 {
			return out
		}
		out = out[:i] + home + out[i+len("%userprofile%"):]
	}
}

// userPathMatches はPath中のentry e が組込対象 entry と「同一ディレクトリ」を指すか判定する。
// 重複skip条件(design-batch3.md §4): 末尾一致(末尾バックラッシュ差を無視)or 拡張なし完全一致。
// さらに %USERPROFILE% 形式と実パス形式の同一指しも重複扱いする(二重組込防止)。
func userPathMatches(e, entry, home string) bool {
	if normalizePathEntry(e) == normalizePathEntry(entry) {
		return true
	}
	if strings.Contains(strings.ToLower(e), "%") || strings.Contains(strings.ToLower(entry), "%") {
		a := normalizePathEntry(expandPortableEntry(e, home))
		b := normalizePathEntry(expandPortableEntry(entry, home))
		return a != "" && a == b
	}
	return false
}

// userPathCompute はPath値(raw: セミコロン区切り)へentryを追加/除去した新値を返す。
// 変化が無い場合は(raw, false)を返す(既存の書式・値を一切書き換えない。setx不使用の根拠)。
func userPathCompute(raw, entry, home string, add bool) (string, bool) {
	if normalizePathEntry(entry) == "" {
		return raw, false
	}
	if add {
		for _, e := range strings.Split(raw, ";") {
			if userPathMatches(e, entry, home) {
				return raw, false // 既に組込済み(skip表示)
			}
		}
		switch {
		case raw == "":
			return entry, true
		case strings.HasSuffix(raw, ";"):
			return raw + entry, true
		default:
			return raw + ";" + entry, true
		}
	}
	var kept []string
	found := false
	for _, e := range strings.Split(raw, ";") {
		if userPathMatches(e, entry, home) {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return raw, false
	}
	return strings.Join(kept, ";"), true
}
