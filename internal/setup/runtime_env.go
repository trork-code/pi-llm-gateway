package setup

import (
	"fmt"
	"os"
	"strings"
)

// Discovery は runtime dir の標準配置を発見した結果。
// 実在しない項目は空文字。State は setup.json が無ければ nil。
//
// env で明示された値(CONFIG_FILE/SECRETS_FILE/AGE_IDENTITY_*/PORT/BIND)は常に
// 呼び出し側(env優先)が解決する。発見値は「env が無いときのフォールバック」で、
// これによりランチャ(up.cmd/up.sh)や対話シェル・pi内の1コマンド実行で
// env を付けずに `gateway up/serve/down` が完結する(runtime dir 統合)。
type Discovery struct {
	Dir      string
	Config   string // config/models.yaml(実在時のみ)
	Secrets  string // secrets/secrets.yaml.age(実在時のみ)
	Identity string // secrets/identity.txt(実在時のみ)
	Log      string // gateway.log の既定出力先(runtime dir 基準なので常に値がある)
	Port     string // setup.json(port) > ランチャ解析。どちらも無ければ空
	Bind     string // setup.json(bind) > ランチャ解析。どちらも無ければ空
	State    *State // setup.json の中身(あれば)
	NotFound bool   // runtime dir 自体が空(何も発見できなかった)
}

// Discover は runtime dir(GATEWAY_RUNTIME_DIR > ~/.pi-gateway)を基準に
// 標準配置(config/secrets/identity/setup.json)とPORT/BINDの既定値を発見する。
// 失敗しない(存在しない項目は空で返す)。呼び出し側は空だった場合の
// 従来既定(CWD相対や8080)にフォールバックする。
func Discover(override string) (Discovery, error) {
	dir, err := ResolveDir(override)
	if err != nil {
		return Discovery{}, err
	}
	paths := Layout(dir)
	d := Discovery{
		Dir:   dir,
		Log:   paths.Log,
		State: nil,
	}
	if _, serr := os.Stat(paths.Config); serr == nil {
		d.Config = paths.Config
	}
	if _, serr := os.Stat(paths.Secrets); serr == nil {
		d.Secrets = paths.Secrets
	}
	if _, serr := os.Stat(paths.Identity); serr == nil {
		d.Identity = paths.Identity
	}
	if st, serr := LoadState(paths); serr == nil && st != nil {
		d.State = st
		if st.Port > 0 {
			d.Port = fmt.Sprint(st.Port)
		}
		if st.Bind != "" {
			d.Bind = st.Bind
		}
	}
	// setup.json 無しのmanual構成: 起動値がランチャ(up.cmd/up.sh)にしか無いことがある
	if d.Port == "" || d.Bind == "" {
		port, bind := LauncherEnv(paths)
		if d.Port == "" {
			d.Port = port
		}
		if d.Bind == "" {
			d.Bind = bind
		}
	}
	d.NotFound = d.Config == "" && d.Secrets == "" && d.Identity == "" && d.State == nil
	return d, nil
}

// LauncherEnv は up.cmd / up.sh から PORT= / BIND= を抽出する。
// wizard以前の手動構成(setup.json 無し)では起動値がランチャにしか無いため。
func LauncherEnv(paths Paths) (port, bind string) {
	data, err := os.ReadFile(paths.Launcher)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "set ") // cmd: "set PORT=18080"
		if rest, ok := cutPrefix(line, "PORT="); ok && port == "" {
			port = strings.Trim(rest, "\" ")
		}
		if rest, ok := cutPrefix(line, "BIND="); ok && bind == "" {
			bind = strings.Trim(rest, "\" ")
		}
	}
	return port, bind
}

// cutPrefix は strings.CutPrefix の互換実装(go 1.20相当を内製する必要が出た場合の隔離点)。
func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return s, false
}
