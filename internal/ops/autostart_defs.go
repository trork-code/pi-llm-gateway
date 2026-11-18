package ops

// 自動起動の登録定義生成(design-batch3.md §2.1)。
//
// 契約(PR#1/docs/proposal-startup.md):
//   - 登録コマンドラインは `<runtime>/bin/gateway(.exe) serve` の絶対パス 1 本のみ
//   - PORT/BIND/identity は焼かない(serve が setup.Discover で自己解決する)
//   - Windows は Task Scheduler XML(<LogonTrigger>+LeastPrivilege+--winhide)、
//     Linux は systemd --user unit(Type=simple+Restart=on-failure)、
//     macOS は LaunchAgent plist(RunAtLoad+KeepAlive)
//
// このファイルは純粋な文字列生成のみ(副作用なし)で、E2E/CI の dry-run(--print)が
// 無副作用であることを保証する。OS への書込み/発火は autostart_install.go。

import "fmt"

// windowsServeArgs はWindows登録時の引数(--winhide: タスク起動時のコンソール点滅防止)。
const windowsServeArgs = "serve --winhide"

// unixServeArgs はUnix登録時の引数。
const unixServeArgs = "serve"

// systemdUnitDef は systemd --user unit(pi-gateway.service)の内容を返す。
// Type=simple+Restart=on-failure(design-batch3.md §2.1)。bin は絶対パス推奨。
func systemdUnitDef(bin string) string {
	return fmt.Sprintf(`[Unit]
Description=pi-llm-gateway (pi coding agent LLM gateway)
Documentation=https://github.com/trork-code/pi-llm-gateway

[Service]
Type=simple
ExecStart=%q %s
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, bin, unixServeArgs)
}

// launchAgentDef は macOS LaunchAgent plist の内容を返す。
// RunAtLoad(ログイン起動)+KeepAlive(終了時再起動)。ログはruntime dirのgateway.logへ。
func launchAgentDef(bin, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, LaunchAgentLabel, bin, logPath, logPath)
}

// windowsTaskDef は Windows Task Scheduler タスク(schtasks /create /xml)の入力XMLを返す。
// <LogonTrigger>(ログオン時に起動)+<RunLevel>LeastPrivilege</RunLevel>(ユーザー権限)+
// ExecutionTimeLimit=PT0S(72時間の自動停止を無効化。常駐プロセスのため)+
// DisallowStartIfOnBatteries/StopIfGoingOnBatteries=false(ノートPCでも常駐)。
// restart-on-fail の <RestartOnFailure> は初版不採用(design-batch3.md §2.1)。
//
// args はそのまま <Arguments> に入る(現在は "serve --winhide")。
func windowsTaskDef(bin, args, username, description string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>%s</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>
`, escapeXMLText(description), username, username, escapeXMLText(bin), args)
}

// escapeXMLText は要素テキスト用の最低限のエスケープ(bin/user名は通常不要だが防御)。
func escapeXMLText(s string) string {
	r := []rune(s)
	out := make([]rune, 0, len(r))
	for _, c := range r {
		switch c {
		case '&':
			out = append(out, []rune("&amp;")...)
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
