// gateway secrets rotate — 隠しCLI。scripts/rotate-secrets.sh のGo化(Windows対応)。
// メニュー「7) secretsローテーション」と同一コア(ops.RotateSecrets)。
//
// 使い方:
//
//	gateway secrets rotate [-recipient <age1...>]... [recipient...]
//	gateway secrets rotate --skip-verify <age1...>   (identity自体をローテーションする時のみ)
//
// recipient省略時: identityで復号できる環境なら現行identityの公開鍵へ再暗号化
// (ファイル更新=鍵齢の更新)。SECRETS_DECRYPT_CMD経由など identity recipients が
// 取れない環境は recipient 必須でfail-closed。
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/awnumar/memguard"

	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// secretsCmd は `gateway secrets rotate` の振り分け口。
func secretsCmd(rest []string, stdout io.Writer) error {
	defer memguard.Purge()
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch sub {
	case "rotate":
		return secretsRotateCmd(rest[1:], stdout)
	default:
		printSecretsUsage(stdout)
		if sub == "" {
			return nil
		}
		return errors.New("secretsのサブコマンドは rotate です")
	}
}

func secretsRotateCmd(rest []string, stdout io.Writer) error {
	var recipients []string
	skipVerify := false
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		switch {
		case tok == "-h" || tok == "-help" || tok == "--help":
			printSecretsUsage(stdout)
			return nil
		case tok == "-skip-verify" || tok == "--skip-verify":
			skipVerify = true
		case tok == "-recipient" || tok == "--recipient" || tok == "-r":
			if i+1 >= len(rest) {
				return fmt.Errorf("-recipient の値がありません")
			}
			recipients = append(recipients, rest[i+1])
			i++
		case strings.HasPrefix(tok, "-recipient="):
			recipients = append(recipients, strings.TrimPrefix(tok, "-recipient="))
		case strings.HasPrefix(tok, "--recipient="):
			recipients = append(recipients, strings.TrimPrefix(tok, "--recipient="))
		case strings.HasPrefix(tok, "-r="):
			recipients = append(recipients, strings.TrimPrefix(tok, "-r="))
		default:
			recipients = append(recipients, tok) // 位置引数recipient(bash版と同一書式)
		}
	}

	dir, err := setup.ResolveDir("")
	if err != nil {
		return err
	}
	paths := setup.Layout(dir)
	loader := doctorSecretsLoader(paths)
	sec, err := loader()
	if err != nil {
		return fmt.Errorf("secretsの復号に失敗: %w", err)
	}
	secretsPath := paths.Secrets
	if v := os.Getenv("SECRETS_FILE"); v != "" {
		secretsPath = v
	}

	res, err := ops.RotateSecrets(ops.RotateOptions{
		Path:       secretsPath,
		Sec:        sec,
		Recipients: recipients,
		Reload:     loader,
		SkipVerify: skipVerify,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "rotated: %s → %d recipient(s)\n", secretsPath, len(res.Recipients))
	for _, r := range res.Recipients {
		fmt.Fprintf(stdout, "  recipient: %s\n", r)
	}
	if res.Verified {
		fmt.Fprintln(stdout, "verified: round-trip decrypt ok (same passphrase input)")
	} else if skipVerify {
		fmt.Fprintln(stdout, "NOT verified (--skip-verify): decrypt manually with the NEW identity before restarting the gateway")
	}
	fmt.Fprintf(stdout, "backup: %s\n", res.BackupPath)
	fmt.Fprintln(stdout, "key age refreshed (doctor check 3 resets); key values and identity file are unchanged.")
	fmt.Fprintln(stdout, "No gateway restart needed — the wrapped contents stay identical.")
	return nil
}

func printSecretsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway secrets rotate [flags] [recipient...]

Re-encrypts secrets (secrets.yaml.age) for the given age recipients, keeps a
.bak backup of the previous file, and verifies a decrypt round-trip before
declaring success (fail-closed: restores the old file on failure).

flags:
  -recipient <age1...>  re-encryption recipient(s); repeatable (bash version parity)
  -skip-verify          only when rotating the identity itself (round-trip skipped)

With no recipients: re-encrypts for the current identity (refreshes the file
age). With SECRETS_DECRYPT_CMD (no identity env) a recipient is required.
Use `+"`gateway keys set/unset`"+` to change key VALUES; `+"`gateway secrets rotate`"+`
re-wraps the file.
`)
}
