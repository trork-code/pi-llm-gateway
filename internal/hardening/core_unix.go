//go:build unix

package hardening

import (
	"fmt"
	"syscall"
)

// DisableCoreDumps はコアダンプを無効化する(多層防御④)。
// クラッシュ時にメモリ内容(復号済み鍵を含む)がダンプファイルとして残るのを防ぐ。
func DisableCoreDumps() error {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &lim); err != nil {
		return fmt.Errorf("RLIMIT_COREの取得に失敗: %w", err)
	}
	lim.Cur = 0
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &lim); err != nil {
		return fmt.Errorf("RLIMIT_COREの設定に失敗: %w", err)
	}
	return nil
}
