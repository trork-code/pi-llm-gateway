// AGE_IDENTITY_CMD(シークレットマネージャ連携)と共通のコマンド実行ヘルパー。
// 「秘密鍵はサーバー上にファイルとして常駐させず、シークレットマネージャから
// 起動の瞬間だけ読み込む」を、ベンダー非依存のコマンド実行で実現する。
package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// IdentityCommand はcmdlineを実行し、その標準出力を秘密鍵として読み取れる
// io.Readerで返す。
//
// 例:
//
//	AGE_IDENTITY_CMD="vault kv get -field=identity secret/pi-llm-gateway"
//	AGE_IDENTITY_CMD="aws secretsmanager get-secret-value --secret-id pi-gateway --query SecretString --output text"
//
// 出力はメモリ上でのみ扱い、ログには出さない。失敗時のエラーにも鍵の内容は含めない。
func IdentityCommand(ctx context.Context, cmdline string) (io.Reader, error) {
	out, err := runCommandOutput(ctx, cmdline)
	if err != nil {
		return nil, fmt.Errorf("AGE_IDENTITY_CMD: %w", err)
	}
	return bytes.NewReader(out), nil
}

// runCommandOutput はcmdlineを実行し、標準出力を返す(共通ヘルパー)。
// SECRETS_DECRYPT_CMD(YubiKeyプラグイン等のage CLI連携)でも再利用する。
// 出力はメモリ上でのみ扱い、ログには出さない。失敗時のエラーにも機微内容は含めない。
func runCommandOutput(ctx context.Context, cmdline string) ([]byte, error) {
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return nil, errors.New("コマンドが空です")
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", cmdline)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdline)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	// 標準エラーは失敗時の手がかりとしてのみ使う(鍵そのものを出すツールは想定しない)
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg != "" {
			return nil, fmt.Errorf("コマンドの実行に失敗: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("コマンドの実行に失敗: %w", err)
	}
	if out.Len() == 0 {
		return nil, errors.New("コマンドの出力が空です")
	}
	if out.Len() > 16<<20 {
		return nil, errors.New("コマンドの出力が大きすぎます")
	}
	return out.Bytes(), nil
}
