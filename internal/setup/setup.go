// Package setup は配布インストールの実体(runtime dir)を管理する。
//
// 配布時の配置規約(design-distribution.md §5):
//
//	~/.pi-gateway/            (Windows: %USERPROFILE%\.pi-gateway)
//	├── setup.json            状態マーカー(非秘匿)。キー値は書かない
//	├── bin/gateway(.exe)     実行バイナリの自己コピー(ランチャはこのパスを呼ぶ)
//	├── config/models.yaml
//	├── secrets/identity.txt  0600 owner-only
//	├── secrets/secrets.yaml.age
//	├── gateway.log
//	└── up.cmd / up.sh        ランチャ(env設定 + gateway up)
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RuntimeDir の既定値。GATEWAY_RUNTIME_DIR で上書きできる。
func defaultRuntimeDir() (string, error) {
	if v := os.Getenv("GATEWAY_RUNTIME_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリを特定できません: %w", err)
	}
	return filepath.Join(home, ".pi-gateway"), nil
}

// ResolveDir はウィザードの配置先ディレクトリを決める(--runtime-dir 指定を優先)。
func ResolveDir(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	return defaultRuntimeDir()
}

// Paths は runtime dir 内の各パス。
type Paths struct {
	Dir       string
	SetupJSON string
	BinDir    string
	Bin       string // bin/gateway(.exe)
	Config    string
	Identity  string
	Secrets   string
	Log       string
	Launcher  string // up.cmd (win) / up.sh (unix)
}

// Layout は dir を基準に各パスを組み立てる。
func Layout(dir string) Paths {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	p := Paths{
		Dir:       dir,
		SetupJSON: filepath.Join(dir, "setup.json"),
		BinDir:    filepath.Join(dir, "bin"),
		Config:    filepath.Join(dir, "config", "models.yaml"),
		Identity:  filepath.Join(dir, "secrets", "identity.txt"),
		Secrets:   filepath.Join(dir, "secrets", "secrets.yaml.age"),
		Log:       filepath.Join(dir, "gateway.log"),
	}
	p.Bin = filepath.Join(p.BinDir, "gateway"+ext)
	if runtime.GOOS == "windows" {
		p.Launcher = filepath.Join(dir, "up.cmd")
	} else {
		p.Launcher = filepath.Join(dir, "up.sh")
	}
	return p
}

// State は setup.json の内容(非秘匿のみ)。
type State struct {
	Version        int      `json:"version"`
	GatewayVersion string   `json:"gateway_version"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	Port           int      `json:"port"`
	Bind           string   `json:"bind"`
	Startup        string   `json:"startup"`       // manual | resident(residentはバッチ3)
	IdentityMode   string   `json:"identity_mode"` // file | env | cmd | passthrough
	Providers      []string `json:"providers"`
	Aliases        []string `json:"aliases"`
	DefaultModel   string   `json:"default_model"`
	PiRegistered   bool     `json:"pi_registered"`
}

// LoadState は setup.json を読む。存在しない場合は nil(未セットアップ)を返す。
func LoadState(paths Paths) (*State, error) {
	data, err := os.ReadFile(paths.SetupJSON)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%sを読めません: %w", paths.SetupJSON, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%sの解析に失敗: %w", paths.SetupJSON, err)
	}
	if st.Version > 1 {
		return nil, fmt.Errorf("%s: 未対応のversion %dです(対応: 1)", paths.SetupJSON, st.Version)
	}
	return &st, nil
}

// SaveState は setup.json を書き込む(created_at 保持、updated_at 更新)。
func SaveState(paths Paths, st *State) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if st.CreatedAt == "" {
		st.CreatedAt = now
	}
	st.UpdatedAt = now
	if st.Version == 0 {
		st.Version = 1
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Dir, 0o755); err != nil {
		return err
	}
	return atomicWrite(paths.SetupJSON, data, 0o644)
}

// IsComplete は setup.json 無しの既存環境(手動構築)を「セットアップ済み」とみなす。
// design-distribution.md §5: config+secrets が揃っていれば完了扱い。
func IsComplete(paths Paths, st *State) bool {
	if st != nil {
		return true
	}
	_, errC := os.Stat(paths.Config)
	_, errS := os.Stat(paths.Secrets)
	return errC == nil && errS == nil
}

// SelfCopy は実行中バイナリを runtime dir/bin へコピーする。
// 既に同一配置(実行バイナリが bin 直下)なら何もしない。
//
// 戻り値 note: 差し替えを延期した理由(旧binが起動中でロック等)。
// Windowsでは稼働中exeの差し替えが失敗するため、旧binを温存し note で伝える
// (サーバーは次回ランチャ起動時に新verへ更新される。doctorが版差を検出する)。
func SelfCopy(paths Paths) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("実行ファイルの特定に失敗: %w", err)
	}
	if ok, err := sameFile(exe, paths.Bin); err == nil && ok {
		return "", nil
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return "", fmt.Errorf("実行ファイルを読めません(%s): %w", exe, err)
	}
	if err := os.MkdirAll(paths.BinDir, 0o755); err != nil {
		return "", err
	}
	if err := atomicWrite(paths.Bin, data, 0o755); err != nil {
		if fileExists(paths.Bin) {
			return fmt.Sprintf("kept the existing %s (in use on Windows) — restart later to refresh it", paths.Bin), nil
		}
		return "", fmt.Errorf("バイナリの配置に失敗(%s): %w", paths.Bin, err)
	}
	return "", nil
}

// sameFile は os.SameFile が cross-device や symlink で扱いにくい Windows 含む単純比較。
func sameFile(a, b string) (bool, error) {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false, errA
	}
	if os.SameFile(ai, bi) {
		return true, nil
	}
	ab, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false, err
	}
	bb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false, err
	}
	return filepath.Clean(ab) == filepath.Clean(bb), nil
}

// AtomicBytes はパッケージ外からも使える原子的書き込み。
func AtomicBytes(path string, data []byte, mode os.FileMode) error {
	return atomicWrite(path, data, mode)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// windowsは実行中ファイルのrename置換に失敗することがある → 一度Removeしてから
	if runtime.GOOS == "windows" && fileExists(path) {
		_ = os.Remove(path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Guard は隠しサブコマンドが未セットアップ環境で実行された場合の導線③。
// 戻り値 true なら「未セットアップの案内を表示して終了してよい」。
//
// ただし明示的なenv(CONFIG_FILE/SECRETS_FILE/GATEWAY_RUNTIME_DIR)がある、
// またはカレントの既定パスにconfig+secretsが揃っている(CI・既存デプロイ)場合はfalse。
func Guard() bool {
	if os.Getenv("CONFIG_FILE") != "" || os.Getenv("SECRETS_FILE") != "" {
		return false
	}
	dir, err := defaultRuntimeDir()
	if err != nil {
		return false
	}
	paths := Layout(dir)
	if _, err := os.Stat(paths.SetupJSON); err == nil {
		return false
	}
	if !IsComplete(paths, nil) {
		return true
	}
	// カレントの既定パス(開発リポジトリ等)にも環境があれば隠しコマンドを許す
	_, errC := os.Stat("config/models.yaml")
	_, errS := os.Stat("secrets/secrets.yaml.age")
	if errC == nil && errS == nil {
		return false
	}
	return true
}

// GuardMessage は Guard() が true のときの案内文。
const GuardMessage = "Gateway is not set up on this machine.\nRun `gateway` (no arguments) to start setup, or `gateway init --provider <p> --api-key env:<VAR> --yes` for non-interactive setup."

// CopyFile は src を dst へ原子的にコピーする(secrets等に再利用)。
func CopyFile(src, dst string, mode os.FileMode) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if strings.HasSuffix(src, ".age") {
		mode = 0o600
	}
	return atomicWrite(dst, data, mode)
}

// ExecutableName は配布バイナリの名前(OS別)。
const ExecutableName = "gateway"
