//go:build !windows

package ops

// ユーザーPATH組込はWindows専用(design-batch3.md §4)。
// unixではシェルrcへの `export PATH=…` 追加案内がinstall出力側で提供されるため、
// ここではno-opの実装(呼んでも副作用なし)を提供する。

func userPathAdd(binDir string) (note string, changed bool, err error) {
	return "PATH組込はWindows専用です", false, nil
}

func userPathRemove(binDir string) (note string, changed bool, err error) {
	return "PATH組込はWindows専用です", false, nil
}

func userPathContains(binDir string) (bool, string, string) {
	return false, "", ""
}
