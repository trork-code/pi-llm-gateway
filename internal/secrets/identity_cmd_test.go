package secrets

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
)

func TestIdentityCommand(t *testing.T) {
	r, err := IdentityCommand(context.Background(), "echo identity-from-manager")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "identity-from-manager" {
		t.Fatalf("output = %q", got)
	}
}

func TestIdentityCommand_Empty(t *testing.T) {
	if _, err := IdentityCommand(context.Background(), "   "); err == nil {
		t.Fatal("空コマンドは拒否されるべき")
	}
}

func TestIdentityCommand_Failure(t *testing.T) {
	_, err := IdentityCommand(context.Background(), "exit 3")
	if err == nil {
		t.Fatal("失敗コマンドはエラーになるべき")
	}
}

func TestIdentityCommand_PlatformShell(t *testing.T) {
	// 実行シェルが意図どおり選ばれていることの確認(Windows=cmd /C, それ以外=sh -c)
	if runtime.GOOS == "windows" {
		if _, err := IdentityCommand(context.Background(), "ver"); err != nil {
			t.Fatalf("cmd.exe経由の実行に失敗: %v", err)
		}
		return
	}
	if _, err := IdentityCommand(context.Background(), "pwd"); err != nil {
		t.Fatalf("sh -cの実行に失敗: %v", err)
	}
}
