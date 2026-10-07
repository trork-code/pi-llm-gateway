//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachSysProcAttr は子プロセスを新しいセッションに退避させ、
// 親(シェル)終了時のSIGHUPハングアップから守る(`gateway up`)。
func detachSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
