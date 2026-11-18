package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestAutostartCmdPrint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GATEWAY_RUNTIME_DIR", dir)
	var out bytes.Buffer
	if err := autostartCmd([]string{"install", "--print"}, &out); err != nil {
		t.Fatalf("autostart --print: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "serve") {
		t.Errorf("--print出力にserveが無い:\n%s", got)
	}
	switch runtime.GOOS {
	case "windows":
		if !strings.Contains(got, "--winhide") || !strings.Contains(got, "<LogonTrigger>") {
			t.Errorf("Windowsの--printに --winhide/<LogonTrigger> が無い:\n%s", got)
		}
	case "linux":
		if !strings.Contains(got, "ExecStart=") || !strings.Contains(got, "Restart=on-failure") {
			t.Errorf("Linuxの--print出力がunitとして不十分:\n%s", got)
		}
	case "darwin":
		if !strings.Contains(got, "dev.pi-gateway") || !strings.Contains(got, "KeepAlive") {
			t.Errorf("macOSの--print出力がplistとして不十分:\n%s", got)
		}
	}
}

func TestAutostartCmdFlagParsing(t *testing.T) {
	f, err := extractAutostartFlags([]string{"install", "--print", "--runtime-dir", "/tmp/x"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.action != "install" || !f.print || f.runtimeDir != "/tmp/x" {
		t.Errorf("flags = %+v", f)
	}
	f2, err := extractAutostartFlags([]string{"--path", "status"})
	if err != nil {
		t.Fatalf("parse2: %v", err)
	}
	if f2.action != "status" || !f2.path {
		t.Errorf("flags2 = %+v", f2)
	}
	if _, err := extractAutostartFlags([]string{"install", "extra"}); err == nil {
		t.Errorf("余分な位置引数を許した")
	}
	if _, err := extractAutostartFlags([]string{"--runtime-dir"}); err == nil {
		t.Errorf("値の無い--runtime-dirを許した")
	}
}

func TestAutostartCmdUnknownAction(t *testing.T) {
	var out bytes.Buffer
	err := autostartCmd([]string{"explode"}, &out)
	if err == nil {
		t.Fatal("未知のactionでエラーになっていない")
	}
	if !strings.Contains(err.Error(), "未知のautostartサブコマンド") {
		t.Errorf("err = %v", err)
	}
}

func TestAutostartCmdNoAction(t *testing.T) {
	var out bytes.Buffer
	if err := autostartCmd(nil, &out); err == nil {
		t.Error("action無しでエラーになっていない")
	}
}

func TestAutostartCmdHelp(t *testing.T) {
	var out bytes.Buffer
	if err := autostartCmd([]string{"-h"}, &out); err != nil {
		t.Fatalf("-h: %v", err)
	}
	if !strings.Contains(out.String(), "usage: gateway autostart") {
		t.Errorf("usage表示が無い:\n%s", out.String())
	}
}
