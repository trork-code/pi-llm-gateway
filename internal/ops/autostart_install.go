package ops

// gateway autostart install/uninstall/status(design-batch3.md §2)。
//
// 登録内容はOS共通で `<runtime>/bin/gateway(.exe) serve` の絶対パス1本のみ。
// serveが setup.Discover でconfig/secrets/identity/PORTを自己解決するため、
// env割当てや鍵の焼き込みはしない(PR#1 プロセス契約)。
//
// 副作用の分離: --print(dry-run)はこのファイルにOS書込み/実行をさせない
// (検出用の DetectAutostart は読み取り専用なので dry-run でも呼んでよい)。
// 実os/execは install/uninstall/PATH の各 OSケース関数内のみ。
//
// 検出(DetectAutostart/autostart.go)はB案の検出+発火パートが担当し、
// 本ファイルは登録定義の適用(install/uninstall)と状態可視化(status)を担う。

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// LaunchAgentLabel はmacOSのLaunchAgent Label(dev.pi-gateway)。
const LaunchAgentLabel = "dev." + AutostartName

// AutostartOptions はautostart CLIのオプション。
type AutostartOptions struct {
	Print bool // dry-run: 登録定義の表示のみ(OSへの副作用なし。E2E/CI用)
	Path  bool // (Windows)ユーザーPATH(HKCU Environment)にbinディレクトリを組込/除去
}

// AutostartResult はinstall/uninstallの操作結果(表示用)。
type AutostartResult struct {
	Kind       string   // schtasks | systemd | launchagent
	Target     string   // OS登録の実体(タスク名/unitパス/plistパス)
	Definition string   // 登録定義本体(--printでstdoutへ出す)
	Notes      []string // 運用注記(置換/次の一手/dormant化など)
}

// AutostartStatus は現在の自動起動状態(status出力/将来doctor統合用)。
type AutostartStatus struct {
	Name       string // 登録名(AutostartName)
	Kind       string // schtasks | systemd | launchagent|(unsupported)
	Registered bool   // serve直接登録済み
	Command    string // 登録コマンドラインの要約
	Legacy     bool   // 旧ランチャ(up.cmd等)参照の旧登録が残っている
	Detail     string // 検出先ヒント(unit/plist/taskパス)
	PathState  string // "added"|"not added"|""(Windows以外)
	PathDetail string
	PathEntry  string // PATH組込に使う/使われたentryの保存形式(%USERPROFILE%形式 or 絶対パス)
	Startup    string // setup.jsonのstartup("resident"/"manual"/""=未記録)
}

// PATHNotApplicable はWindows以外のOSでPATH組込が無効であることを示す値。
const PATHNotApplicable = ""

// AutostartInstall はOSへserve直接登録を行う(--print時は登録定義の返却のみ)。
func AutostartInstall(paths setup.Paths, opts AutostartOptions) (AutostartResult, error) {
	switch runtime.GOOS {
	case "windows":
		return installWindows(paths, opts)
	case "linux":
		return installSystemd(paths, opts)
	case "darwin":
		return installLaunchAgent(paths, opts)
	default:
		return AutostartResult{}, fmt.Errorf("autostart install は %s では対応していません", runtime.GOOS)
	}
}

// AutostartUninstall はOSからserve直接登録を除去する(ランチャ/鍵/設定は触らない)。
func AutostartUninstall(paths setup.Paths, opts AutostartOptions) (AutostartResult, error) {
	switch runtime.GOOS {
	case "windows":
		return uninstallWindows(paths, opts)
	case "linux":
		return uninstallSystemd(paths)
	case "darwin":
		return uninstallLaunchAgent(paths)
	default:
		return AutostartResult{}, fmt.Errorf("autostart uninstall は %s では対応していません", runtime.GOOS)
	}
}

// AutostartStatusInfo は登録状態・PATH組込状態・startup設定を検査する(読み取りのみ)。
func AutostartStatusInfo(paths setup.Paths) (AutostartStatus, error) {
	reg, err := DetectAutostart(paths)
	if err != nil {
		return AutostartStatus{}, err
	}
	st, lerr := setup.LoadState(paths) // LoadStateはnilを(存在しない場合に)返す
	if lerr != nil {
		// 壊れたsetup.jsonでも登録は続行できる(startupのdormant判定はenv/フラグでも代替するため)
		st = nil
	}
	out := AutostartStatus{
		Name:       AutostartName,
		Kind:       statusKind(),
		Registered: reg.Registered,
		Command:    reg.Command,
		Legacy:     reg.Legacy,
		Detail:     reg.Detail,
	}
	if st != nil {
		out.Startup = st.Startup
	}
	switch runtime.GOOS {
	case "windows":
		added, detail, literal := userPathContains(paths.BinDir)
		out.PathEntry = literal
		if added {
			out.PathState = "added"
		} else {
			out.PathState = "not added"
		}
		if detail != "" {
			out.PathDetail = detail
		}
	case "linux", "darwin":
		out.PathState = PATHNotApplicable
	}
	return out, nil
}

func statusKind() string {
	switch runtime.GOOS {
	case "windows":
		return "schtasks"
	case "linux":
		return "systemd"
	case "darwin":
		return "launchagent"
	default:
		return runtime.GOOS
	}
}

// ensureBin は登録前にbinの存在を確認する。無ければ実行中バイナリをコピー(SelfCopy相当)。
// --print時はコピーしない(dry-run副作用なし)。
func ensureBin(paths setup.Paths, opts AutostartOptions) (string, []string, error) {
	if _, err := os.Stat(paths.Bin); err == nil {
		return paths.Bin, nil, nil
	}
	if opts.Print {
		// dry-run: 存在チェックに留める(将来の実配置はここでSelfCopyされる)
		return paths.Bin, []string{fmt.Sprintf("binがありません(%s)—実行時はgateway自身が自己コピーします", paths.Bin)}, nil
	}
	note, err := setup.SelfCopy(paths)
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(paths.Bin); err != nil {
		return "", nil, fmt.Errorf("binの配置に失敗しました(%s)", paths.Bin)
	}
	notes := []string{}
	if note != "" {
		notes = append(notes, note)
	}
	return paths.Bin, notes, nil
}

// startupResident はsetup.jsonがあれば startup="resident" に更新する(ドーマント有効化)。
// setup.jsonが無い手動環境では何もしない(§2)。
func startupResident(paths setup.Paths) string {
	st, err := setup.LoadState(paths)
	if err != nil || st == nil {
		return "" // setup.jsonが無い=手動環境。residentを書けない(dormantはenv/フラグで)
	}
	if strings.EqualFold(st.Startup, "resident") {
		return ""
	}
	st.Startup = "resident"
	if err := setup.SaveState(paths, st); err != nil {
		return fmt.Sprintf("setup.jsonのstartup更新に失敗しました(%v)", err)
	}
	return "setup.jsonのstartupをresidentに更新しました — serveは鍵を復号せず待機します" +
		"(dormant。無効化は `gateway serve --no-dormant` または setup.json startup=manual)"
}

// --- Linux(systemd --user) --------------------------------------------------

func systemdUserDir() (string, error) {
	conf, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("ユーザーconfigディレクトリを特定できません: %w", err)
	}
	return filepath.Join(conf, "systemd", "user"), nil
}

func installSystemd(paths setup.Paths, opts AutostartOptions) (AutostartResult, error) {
	bin, notes, err := ensureBin(paths, opts)
	if err != nil {
		return AutostartResult{}, err
	}
	res := AutostartResult{Kind: "systemd", Definition: systemdUnitDef(bin), Notes: notes}
	unitDir, err := systemdUserDir()
	if err != nil {
		return res, err
	}
	unitPath := filepath.Join(unitDir, AutostartName+".service")
	res.Target = unitPath
	if opts.Print {
		return res, nil
	}
	if reg, derr := DetectAutostart(paths); derr == nil && reg.Registered {
		res.Notes = append(res.Notes, "既存の登録を置き換えます: "+unitPath)
	}
	if err := setup.AtomicBytes(unitPath, []byte(res.Definition), 0o644); err != nil {
		return res, fmt.Errorf("unitの書込みに失敗(%s): %w", unitPath, err)
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return res, fmt.Errorf("systemctl --user daemon-reloadに失敗: %s(%w)", strings.TrimSpace(string(out)), err)
	}
	if _, err := runOSCommand("systemctl", "--user", "enable", AutostartName); err != nil {
		return res, err
	}
	if note := startupResident(paths); note != "" {
		res.Notes = append(res.Notes, note)
	}
	res.Notes = append(res.Notes,
		fmt.Sprintf("登録しました(unit %s)。今すぐ起動するには `gateway up`(=%q)を実行", unitPath, systemctlStart()))
	res.Notes = append(res.Notes,
		"PATH組込はWindows専用です — unixはシェルrcに export PATH=\""+filepath.Dir(bin)+":$PATH\" を追加してください")
	return res, nil
}

func uninstallSystemd(paths setup.Paths) (AutostartResult, error) {
	res := AutostartResult{Kind: "systemd"}
	unitDir, err := systemdUserDir()
	if err != nil {
		return res, err
	}
	unitPath := filepath.Join(unitDir, AutostartName+".service")
	res.Target = unitPath
	if out, err := exec.Command("systemctl", "--user", "disable", "--now", AutostartName).CombinedOutput(); err != nil {
		// 未登録/未実装環境は許容(idempotent)
		if !strings.Contains(string(out), "Failed to disable unit: Unit "+AutostartName+".service does not exist") && !fileNotExists(unitPath) {
			res.Notes = append(res.Notes, fmt.Sprintf("systemctl --user disable --now が失敗しました(続行します): %s", strings.TrimSpace(string(out))))
		}
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return res, fmt.Errorf("unitの削除に失敗(%s): %w", unitPath, err)
	}
	if _, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		res.Notes = append(res.Notes, "systemctl --user daemon-reloadに失敗しました(unitは削除済み)")
	}
	res.Notes = append(res.Notes, "登録を解除しました(ランチャ・鍵・設定は変更していません)")
	return res, nil
}

func systemctlStart() string { return "systemctl --user start " + AutostartName }

func fileNotExists(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// --- macOS(LaunchAgent) ------------------------------------------------------

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリを特定できません: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"), nil
}

func installLaunchAgent(paths setup.Paths, opts AutostartOptions) (AutostartResult, error) {
	bin, notes, err := ensureBin(paths, opts)
	if err != nil {
		return AutostartResult{}, err
	}
	agentPath, err := launchAgentPath()
	if err != nil {
		return AutostartResult{}, err
	}
	res := AutostartResult{Kind: "launchagent", Target: agentPath,
		Definition: launchAgentDef(bin, paths.Log), Notes: notes}
	if opts.Print {
		return res, nil
	}
	if reg, derr := DetectAutostart(paths); derr == nil && reg.Registered {
		res.Notes = append(res.Notes, "既存の登録を置き換えます: "+agentPath)
	}
	if err := setup.AtomicBytes(agentPath, []byte(res.Definition), 0o644); err != nil {
		return res, fmt.Errorf("plistの書込みに失敗(%s): %w", agentPath, err)
	}
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d", uid)
	if out, err := exec.Command("launchctl", "bootout", domain+"/"+LaunchAgentLabel).CombinedOutput(); err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("launchctl bootout(未ロードなら正常): %s", condenseArgs(out)))
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain, agentPath).CombinedOutput(); err != nil {
		return res, fmt.Errorf("launchctl bootstrapに失敗: %s(%w)", strings.TrimSpace(string(out)), err)
	}
	if note := startupResident(paths); note != "" {
		res.Notes = append(res.Notes, note)
	}
	res.Notes = append(res.Notes,
		"登録しました(KeepAlive=true)。`gateway down` してもlaunchctlが再起動します — 停止したい場合は `gateway autostart uninstall` を実行してください")
	return res, nil
}

func uninstallLaunchAgent(paths setup.Paths) (AutostartResult, error) {
	res := AutostartResult{Kind: "launchagent"}
	agentPath, err := launchAgentPath()
	if err != nil {
		return res, err
	}
	res.Target = agentPath
	uid := os.Getuid()
	if out, err := exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, LaunchAgentLabel)).CombinedOutput(); err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("launchctl bootout(未ロードなら正常): %s", condenseArgs(out)))
	}
	if err := os.Remove(agentPath); err != nil && !os.IsNotExist(err) {
		return res, fmt.Errorf("plistの削除に失敗(%s): %w", agentPath, err)
	}
	res.Notes = append(res.Notes, "登録を解除しました(ランチャ・鍵・設定は変更していません)")
	return res, nil
}

// --- Windows(Task Scheduler) ---------------------------------------------------

func installWindows(paths setup.Paths, opts AutostartOptions) (AutostartResult, error) {
	bin, notes, err := ensureBin(paths, opts)
	if err != nil {
		return AutostartResult{}, err
	}
	username := ""
	if u, uerr := user.Current(); uerr == nil {
		username = u.Username
	}
	res := AutostartResult{Kind: "schtasks", Notes: notes,
		Definition: windowsTaskDef(bin, windowsServeArgs, username, "pi-llm-gateway serve (login autostart)")}
	res.Target = fmt.Sprintf("schtasks:/tn=%s", AutostartName)
	if opts.Print {
		return res, nil
	}
	reg, err := DetectAutostart(paths)
	if err != nil {
		return res, err
	}
	// 旧ランチャ(up.cmd等)参照のHKCU Run値は置換(§2.1): 値を削除して新登録を書く
	if reg.Legacy {
		if out, derr := exec.Command("reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
			"/v", AutostartName, "/f").CombinedOutput(); derr != nil {
			return res, fmt.Errorf("旧HKCU Run登録の削除に失敗: %s(%w)", strings.TrimSpace(string(out)), derr)
		}
		res.Notes = append(res.Notes, "旧登録(HKCU Run がup.cmdを参照)を削除しました — 新登録はserveを直接実行します")
	}
	if reg.Registered {
		if out, derr := exec.Command("schtasks", "/delete", "/tn", AutostartName, "/f").CombinedOutput(); derr != nil {
			return res, fmt.Errorf("既存タスク %s の削除に失敗: %s(%w)", AutostartName, strings.TrimSpace(string(out)), derr)
		}
		res.Notes = append(res.Notes, "既存タスクを置き換えます(削除して新定義を作成)")
	}
	tmp, err := os.CreateTemp("", AutostartName+"-task-*.xml")
	if err != nil {
		return res, fmt.Errorf("一時XMLファイルの作成に失敗: %w", err)
	}
	tmpName := tmp.Name()
	if werr := writeUTF16LEBOM(tmp, res.Definition); werr != nil {
		tmp.Close()
		os.Remove(tmpName)
		return res, werr
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return res, err
	}
	if out, cerr := exec.Command("schtasks", "/create", "/tn", AutostartName, "/xml", tmpName, "/f").CombinedOutput(); cerr != nil {
		os.Remove(tmpName)
		return res, fmt.Errorf("schtasks /create に失敗: %s(%w)", strings.TrimSpace(string(out)), cerr)
	}
	os.Remove(tmpName)
	if note := startupResident(paths); note != "" {
		res.Notes = append(res.Notes, note)
	}
	res.Notes = append(res.Notes,
		fmt.Sprintf("タスク %s を登録しました(ログオン時+`schtasks /run`で発火。コンソールは--winhideで非表示)。今すぐ起動するには `gateway up` 実行", AutostartName))
	if opts.Path {
		note, changed, err := userPathAdd(paths.BinDir)
		if err != nil {
			return res, err
		}
		_ = changed // skip(重複)時も案内文言は同じで良い
		res.Notes = append(res.Notes, note)
	} else {
		res.Notes = append(res.Notes, "ヒント: `--path` で binディレクトリをユーザーPATHに追加できます("+paths.BinDir+")")
	}
	return res, nil
}

func uninstallWindows(paths setup.Paths, opts AutostartOptions) (AutostartResult, error) {
	res := AutostartResult{Kind: "schtasks",
		Target: fmt.Sprintf("schtasks:/tn=%s", AutostartName)}
	if opts.Print {
		res.Notes = append(res.Notes, "--printで表示する登録定義はありません(uninstall)")
		return res, nil
	}
	reg, err := DetectAutostart(paths)
	if err != nil {
		return res, err
	}
	if out, derr := exec.Command("schtasks", "/delete", "/tn", AutostartName, "/f").CombinedOutput(); derr != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("タスク %s は存在しない/削除できませんでした: %s", AutostartName, strings.TrimSpace(string(out))))
	} else {
		res.Notes = append(res.Notes, fmt.Sprintf("タスク %s を削除しました", AutostartName))
	}
	if reg.Legacy {
		if out, derr := exec.Command("reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
			"/v", AutostartName, "/f").CombinedOutput(); derr != nil {
			res.Notes = append(res.Notes, "旧HKCU Run登録の削除に失敗しました: "+strings.TrimSpace(string(out)))
		} else {
			res.Notes = append(res.Notes, "旧登録(HKCU Run がup.cmdを参照)を削除しました")
		}
	}
	if opts.Path {
		note, changed, err := userPathRemove(paths.BinDir)
		if err != nil {
			return res, err
		}
		res.Notes = append(res.Notes, note)
		_ = changed
	}
	res.Notes = append(res.Notes, "登録を解除しました(ランチャ・鍵・設定は変更していません)")
	return res, nil
}

// --- 共有ヘルパ -----------------------------------------------------------------

// runOSCommand は外部コマンドを実行し表示用コマンドラインを返す(発火・enable等)。
func runOSCommand(name string, args ...string) (string, error) {
	full := append([]string{name}, args...)
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return strings.Join(full, " "), fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.Join(full, " "), nil
}

// condenseArgs は外部コマンドの出力を1行に圧縮する(表示用)。
func condenseArgs(s []byte) string {
	f := strings.Fields(string(s))
	return strings.Join(f, " ")
}

const (
	utf16BOM1 byte = 0xFF
	utf16BOM2 byte = 0xFE
)

// writeUTF16LEBOM は文字列をBOM付きUTF-16LEで書き込む。
// schtasks /xml はUTF-16(BOM付き)が素直に読める(Windowsネイティブ形式)。
// unicode/utf16(全OSで共通)を使用(syscall.UTF16FromStringはWindows専用ビルドのため)。
// NULはXML定義に登場しないため、surrogateペアとコードポイント直書きで足りる。
func writeUTF16LEBOM(f *os.File, s string) error {
	if _, err := f.Write([]byte{utf16BOM1, utf16BOM2}); err != nil {
		return err
	}
	var buf []byte
	for _, r := range s {
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			buf = appendUTF16LE(buf, uint16(hi))
			buf = appendUTF16LE(buf, uint16(lo))
			continue
		}
		buf = appendUTF16LE(buf, uint16(r))
	}
	_, err := f.Write(buf)
	return err
}

func appendUTF16LE(buf []byte, u uint16) []byte {
	return append(buf, byte(u), byte(u>>8))
}
