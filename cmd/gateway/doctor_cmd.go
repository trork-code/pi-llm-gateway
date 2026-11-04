// gateway doctor — 隠しCLI。「6) Diagnose」(メニュー)と同一コア(ops.Doctor)。
//
// 出力契約(design-distribution.md §10):
//   - 10検査項目を [ok]/[warn]/[fail]/[skip] 行で出力し、末尾にfix提案
//   - exit code は fail>0 → 1、warn>0 → 0(スクリプトから自動判定できる)
package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/awnumar/memguard"

	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
	"github.com/trork-code/pi-llm-gateway/internal/wizard"
)

// doctorCmd は診断を実行して exit code を返す(fail>0 → 1)。
func doctorCmd(rest []string, stdout io.Writer) (int, error) {
	defer memguard.Purge()
	addr := ""
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		switch {
		case tok == "-h" || tok == "-help" || tok == "--help":
			printDoctorUsage(stdout)
			return 0, nil
		case tok == "-addr" || tok == "--addr":
			if i+1 >= len(rest) {
				return 1, fmt.Errorf("-addr の値がありません")
			}
			addr = rest[i+1]
			i++
		case strings.HasPrefix(tok, "-addr="):
			addr = strings.TrimPrefix(tok, "-addr=")
		case strings.HasPrefix(tok, "--addr="):
			addr = strings.TrimPrefix(tok, "--addr=")
		default:
			return 1, fmt.Errorf("未知の引数 %q(doctor は位置引数を取りません)", tok)
		}
	}

	dir, err := setup.ResolveDir("")
	if err != nil {
		return 1, err
	}
	paths := setup.Layout(dir)
	opts := ops.DoctorOptions{
		Dir:          dir,
		Addr:         addr,
		CLIVersion:   appVersion,
		PiModelsPath: wizard.PiModelsPath(),
		MaxAgeDays:   int64(envInt("SECRETS_MAX_AGE_DAYS", 90)),
		LoadSecrets:  doctorSecretsLoader(paths),
		Stdout:       stdout,
	}
	rep, err := ops.Doctor(opts)
	if err != nil {
		return 1, err
	}
	if rep.Failures() > 0 {
		return 1, nil
	}
	return 0, nil
}

func printDoctorUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway doctor [flags]

Runs the 10-point environment diagnostic and prints one [ok]/[warn]/[fail]/
[skip] line per check plus fix hints at the end. Exit code is 1 when at least
one check failed (warnings do not affect it). Same core as menu "6) Diagnose".

flags:
  -addr URL   /healthz endpoint override (default: GATEWAY_ADDR > GATEWAY_RELOAD_ADDR > http://127.0.0.1:<PORT|8080>)
`)
}

// doctorSecretsLoader はdoctor/rotate用の復号器。
// runtime dirのidentity(またはsecrets実体)があればそこを見る。
// 無ければ環境変数経路(CONFIG_FILE/SECRETS_FILE/AGE_IDENTITY_*)/SECRETS_DECRYPT_CMD)
// にフォールバックする(サーバー・keys CLIと同じ規約)。
func doctorSecretsLoader(paths setup.Paths) func() (*secrets.Secrets, error) {
	useRuntime := pathExists(paths.Identity) || pathExists(paths.Secrets)
	if !useRuntime {
		return func() (*secrets.Secrets, error) {
			return loadSecretsForCLI(slog.Default())
		}
	}
	return func() (*secrets.Secrets, error) {
		dc := os.Getenv("SECRETS_DECRYPT_CMD")
		opts := secrets.LoadOptions{
			EncryptedPath:  paths.Secrets,
			Passphrase:     os.Getenv("AGE_PASSPHRASE"),
			DecryptCommand: dc,
		}
		if dc == "" {
			data, err := os.ReadFile(paths.Identity)
			if err != nil {
				return nil, fmt.Errorf("identityを読めません(%s): %w", paths.Identity, err)
			}
			opts.Identity = bytes.NewReader(data)
			sec, err := secrets.Load(opts)
			wipeBytes(data) // identityの生バイトは用が済みしだい消す(ベストエフォート)
			return sec, err
		}
		return secrets.Load(opts)
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
