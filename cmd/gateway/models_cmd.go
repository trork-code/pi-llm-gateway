// gateway models — 隠しCLI。config/models.yaml 基準のエイリアス表を表示する。
// メニュー「3) エイリアス管理」の表示部と同一情報源。
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// modelsCmd はエイリアス表を表示する(default は * 印)。
func modelsCmd(rest []string, stdout io.Writer) error {
	for _, tok := range rest {
		if tok == "-h" || tok == "-help" || tok == "--help" {
			printModelsUsage(stdout)
			return nil
		}
		return fmt.Errorf("未知の引数 %q(models はフラグを取りません)", tok)
	}
	cfgPath, err := displayConfigPath()
	if err != nil {
		return err
	}
	rows, err := ops.ListAliases(cfgPath)
	if err != nil {
		return fmt.Errorf("%w — `gateway` (no arguments) でセットアップ/再実行してください", err)
	}
	ops.PrintModelsTable(stdout, cfgPath, rows)
	return nil
}

// displayConfigPath は runtime config を優先し、無ければ CONFIG_FILE env を見る。
func displayConfigPath() (string, error) {
	dir, err := setup.ResolveDir("")
	if err != nil {
		return "", err
	}
	cfgPath := setup.Layout(dir).Config
	if _, err := os.Stat(cfgPath); err == nil {
		return cfgPath, nil
	}
	if v := os.Getenv("CONFIG_FILE"); v != "" {
		return v, nil
	}
	return cfgPath, nil
}

func printModelsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: gateway models

Prints the alias table from config/models.yaml (the current config view;
`+"`/model`"+` in pi reads the same ids). "*" marks the default alias.
`)
}
