// AGE_IDENTITY_CMD(多層防御②)の実装。
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
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return nil, errors.New("AGE_IDENTITY_CMDが空です")
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
			return nil, fmt.Errorf("AGE_IDENTITY_CMDの実行に失敗: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("AGE_IDENTITY_CMDの実行に失敗: %w", err)
	}
	if out.Len() == 0 {
		return nil, errors.New("AGE_IDENTITY_CMDの出力が空です")
	}
	return bytes.NewReader(out.Bytes()), nil
}
