package hardening

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windowsではパーミッション検証をスキップ")
	}
	p := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnerOnly(p); err != nil {
		t.Fatalf("0600は許可されるべき: %v", err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnerOnly(p); err == nil {
		t.Fatal("0644は拒否されるべき")
	}
}

func TestCheckNotWorldWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windowsではパーミッション検証をスキップ")
	}
	p := filepath.Join(t.TempDir(), "secrets.yaml.age")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckNotWorldWritable(p); err != nil {
		t.Fatalf("0600は許可されるべき: %v", err)
	}
	if err := os.Chmod(p, 0o602); err != nil {
		t.Fatal(err)
	}
	if err := CheckNotWorldWritable(p); err == nil {
		t.Fatal("他者書き込み可能(0o602)は拒否されるべき")
	}
}
