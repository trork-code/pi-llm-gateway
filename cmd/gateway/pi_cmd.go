package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/wizard"
)

// piCmd は隠しCLI `gateway pi install|uninstall`。
// ウィザードの「4) pi再登録」と同一コア(ops.PiInstall / ops.PiUninstall)を呼ぶ。
func piCmd(rest []string, stdout io.Writer) error {
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch sub {
	case "install":
		return piInstallCmd(stdout)
	case "uninstall":
		return piUninstallCmd(stdout)
	default:
		printPiUsage(stdout)
		if sub == "" {
			return nil
		}
		return fmt.Errorf("piのサブコマンドは install / uninstall です")
	}
}

func printPiUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway pi install|uninstall

Re-merges the `+"`gateway`"+` provider block into pi's models.json (aliases
derived from config/models.yaml). Existing gateway keys are reused when they
match secrets; without a key in secrets, run `+"`gateway init`"+` first.
`)
}

func piInstallCmd(stdout io.Writer) error {
	paths, sec, _, err := wizard.MenuRuntime()
	if err != nil {
		return err
	}
	aliases, err := wizard.AliasesForPi(paths.Config)
	if err != nil {
		return err
	}
	if len(aliases) == 0 {
		return errors.New("config/models.yaml にエイリアスがありません")
	}
	res, err := ops.PiInstall(ops.PiInstallOptions{
		ModelsPath: wizard.PiModelsPath(),
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%s/v1", wizard.MenuPort(paths)),
		Aliases:    aliases,
		GatewayKey: piKeyOf(sec),
		HasGatewayKey: func(k string) bool {
			for _, kk := range sec.AllGatewayKeys() {
				if kk == k {
					return true
				}
			}
			return false
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "pi models.json updated: %d alias models (gateway key %s%s).\n",
		res.ModelsCount, wizard.Fingerprint(res.GatewayKey), reusedMark(res.ReusedKey))
	if res.BackupPath != "" {
		fmt.Fprintf(stdout, "backup: %s\n", res.BackupPath)
	}
	fmt.Fprintln(stdout, "If pi was already open, restart pi to pick up changes.")
	return nil
}

func piUninstallCmd(stdout io.Writer) error {
	if _, _, _, err := wizard.MenuRuntime(); err != nil {
		return err
	}
	if err := ops.PiUninstall(wizard.PiModelsPath()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed the gateway provider from %s (backups kept).\n", wizard.PiModelsPath())
	return nil
}

func piKeyOf(sec *secrets.Secrets) string {
	ks := sec.AllGatewayKeys()
	if len(ks) == 0 {
		return ""
	}
	return ks[0]
}

func reusedMark(reused bool) string {
	if reused {
		return ", kept"
	}
	return ""
}
