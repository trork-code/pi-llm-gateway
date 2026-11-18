package ops

// autostart(design-batch3.md §1.1/§2): serve 直接登録の検出と発火。
// 登録定義の生成と install/uninstall/status CLI はバッチ3 C案がこのファイルに追記する(分担:
// このファイルの検出+発火はB案コミットで先行している。Cタブは push 前に pull --rebase すること)。
//
// 契約(PR#1/docs/proposal-startup.md):
//   - 登録コマンドラインは <runtime>/bin/gateway(.exe) serve(env 割当て無し。PORT/identity は serve が発見)
//   - `gateway up` は fork せず、登録済みなら OS API で発火し、未登録なら起動手順を案内する

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"

	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// AutostartRegistration はserve直接登録の検出結果。
type AutostartRegistration struct {
	Registered bool   // serve直接登録がある(pi-gateway)
	Command    string // OS記録から取り出したコマンドラインの要約(デバッグ表示用)
	Legacy     bool   // 旧ランチャ(up.cmd等)を参照する登録がある=置換推奨
	Detail     string // 検出先のヒント(unit/plist/task 等のパス)
}

// AutostartName は登録名(systemd unit名/LaunchAgent ID/Task Schedulerタスク名の共通名)。
const AutostartName = "pi-gateway"

// DetectAutostart はOSごとの自動起動登録を検査する(副作用なし。読み取りのみ)。
func DetectAutostart(paths setup.Paths) (AutostartRegistration, error) {
	switch runtime.GOOS {
	case "linux":
		return detectSystemd(paths)
	case "darwin":
		return detectLaunchAgent(paths)
	case "windows":
		return detectWindows()
	default:
		return AutostartRegistration{}, nil
	}
}

// AutostartFire はOS API経由でserveを起動する(forkしない: OSがsupervisor)。
// 返す文字列は実行したコマンド(表示用)。
func AutostartFire() (string, error) {
	switch runtime.GOOS {
	case "linux":
		return runFire("systemctl", "--user", "start", AutostartName)
	case "darwin":
		uid := os.Getuid()
		return runFire("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d", uid), "dev."+AutostartName)
	case "windows":
		return runFire("schtasks", "/run", "/tn", AutostartName)
	default:
		return "", fmt.Errorf("未対応のOSでfireは利用不可: %s", runtime.GOOS)
	}
}

func runFire(name string, args ...string) (string, error) {
	full := append([]string{name}, args...)
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return strings.Join(full, " "), fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.Join(full, " "), nil
}

func detectSystemd(paths setup.Paths) (AutostartRegistration, error) {
	reg := AutostartRegistration{Detail: "~/.config/systemd/user/" + AutostartName + ".service"}
	data, err := os.ReadFile(reg.Detail)
	if err != nil {
		return reg, nil // 未登録(エラーは無視: 存在しない=未登録)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
			reg.Command = rest
			break
		}
	}
	// serve直接登録: 参照がgateway(.exe)で引数に serve を含む
	if reg.Command != "" && strings.Contains(reg.Command, "serve") &&
		(strings.Contains(reg.Command, "gateway") || strings.Contains(reg.Command, paths.Bin)) {
		reg.Registered = true
	}
	if !reg.Registered {
		reg.Command = ""
	}
	return reg, nil
}

func detectLaunchAgent(paths setup.Paths) (AutostartRegistration, error) {
	reg := AutostartRegistration{Detail: "~/Library/LaunchAgents/dev." + AutostartName + ".plist"}
	data, err := os.ReadFile(reg.Detail)
	if err != nil {
		return reg, nil
	}
	text := string(data)
	if textContainsCmdServe(text, paths.Bin) {
		reg.Registered = true
		// Command要約: ProgramArgumentsの中身を簡易抽出
		if i := strings.Index(text, "<array>"); i >= 0 {
			j := strings.Index(text[i:], "</array>")
			if j >= 0 {
				reg.Command = condenseTags(text[i : i+j])
			}
		}
	}
	return reg, nil
}

func detectWindows() (AutostartRegistration, error) {
	reg := AutostartRegistration{Detail: "schtasks /query /tn " + AutostartName}
	out, err := exec.Command("schtasks", "/query", "/tn", AutostartName, "/xml").CombinedOutput()
	if err != nil {
		// タスクが無い=未登録。エラー文言自体には依存しない
		return reg, nil
	}
	text := string(out)
	if textContainsCmdServe(text, "") {
		reg.Registered = true
		if i := strings.Index(text, "<Command>"); i >= 0 {
			j := strings.Index(text[i:], "</Command>")
			if j >= 0 {
				// Argumentsは別要素のためCommand行のみ簡易表示
				reg.Command = condenseTags(text[i : i+j])
				if k := strings.Index(text, "<Arguments>"); k >= 0 {
					if l := strings.Index(text[k:], "</Arguments>"); l >= 0 {
						reg.Command += " " + condenseTags(text[k:k+l])
					}
				}
			}
		}
	}
	// 旧ランチャ参照のHKCU Run値(up.cmd等)は置換推奨の理由として記録
	if u, err := user.Current(); err == nil {
		key := `Software\Microsoft\Windows\CurrentVersion\Run`
		if out, err := exec.Command("reg", "query", "HKCU\\"+key, "/v", AutostartName).CombinedOutput(); err == nil {
			if strings.Contains(string(out), "up.cmd") {
				reg.Legacy = true
			}
		}
		_ = u
	}
	return reg, nil
}

// textContainsCmdServe は登録定義(自由形式テキスト)が gateway serve の実行を表すか概ね判定する。
func textContainsCmdServe(text, binPath string) bool {
	text = strings.ToLower(text)
	hasExe := binPath != "" && strings.Contains(strings.ToLower(binPath), ".exe")
	_ = hasExe
	return strings.Contains(text, "serve") && (strings.Contains(text, "gateway") ||
		strings.Contains(text, strings.ToLower(binPath)))
}

// condenseTags はXML断片からタグを剥がして1行にする。
func condenseTags(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		s = s[:start] + " " + s[start+end+1:]
	}
	return strings.TrimSpace(s)
}
