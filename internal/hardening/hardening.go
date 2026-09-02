// Package hardening はAPIキー保護の多層防御(アーキテクチャ仕様書§7.5)のうち、
// コード側で実装できるOS・プロセスレイヤーの対策を集約する。
//
// 対応する層:
//   - ③ OS・ファイルシステム: ファイル権限の検証(CheckOwnerOnly / CheckNotWorldWritable)
//   - ④ プロセス・メモリ: コアダンプの無効化(DisableCoreDumps)
package hardening

import (
	"fmt"
	"os"
	"runtime"
)

// CheckOwnerOnly はPOSIX環境でpathが所有者のみアクセス可能(0600相当)かを確認する。
// 秘密鍵(identity)など、他人に読まれてはならないファイルに対して使う。
// Windowsではパーミッションモデルが異なるため常にnilを返す。
func CheckOwnerOnly(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s の状態を確認できません: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s の権限が %o です。所有者のみに絞ってください: chmod 600 %s", path, perm, path)
	}
	return nil
}

// CheckNotWorldWritable はpathが誰でも書き換え可能でないことを確認する。
// secrets.yaml.age(暗号化済み)はリポジトリにコミット可能なため読み取りは許容するが、
// 書き換え可能な状態は鍵すり替えの危険があるため拒否する。
func CheckNotWorldWritable(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s の状態を確認できません: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%s の権限が %o です。グループ/他者による書き込みを禁止してください: chmod 600 %s", path, perm, path)
	}
	return nil
}
