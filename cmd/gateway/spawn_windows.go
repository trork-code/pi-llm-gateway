//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detachSysProcAttr はWindowsで子プロセスをコンソール・ジョブから切り離す。
// 親の終了後もgatewayを生存させる(`gateway up`)。
func detachSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x00000008 // DETACHED_PROCESS
}
