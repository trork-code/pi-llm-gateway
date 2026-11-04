// `gateway models` / メニュー表示用のエイリアス表(config/models.yaml 基準)。
// pi側ではなくconfigの現状をそのまま見せる(設計: エイリアスのsource of truthはconfig)。
package ops

import (
	"fmt"
	"io"
	"sort"

	"github.com/trork-code/pi-llm-gateway/internal/config"
)

// AliasRow はエイリアス表の1行。
type AliasRow struct {
	Alias    string
	Provider string
	Model    string
	Default  bool
}

// ListAliases は config/models.yaml を読んでエイリアス行(ソート済み)を返す。
// default_model は Default フラグに反映。
func ListAliases(configPath string) ([]AliasRow, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	rows := make([]AliasRow, 0, len(cfg.Models))
	for alias, mc := range cfg.Models {
		rows = append(rows, AliasRow{
			Alias:    alias,
			Provider: mc.Provider,
			Model:    mc.Model,
			Default:  alias == cfg.DefaultModel,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Alias < rows[j].Alias })
	return rows, nil
}

// PrintModelsTable はエイリアス表を表示する(default は * 印)。
func PrintModelsTable(w io.Writer, configPath string, rows []AliasRow) {
	fmt.Fprintf(w, "config: %s\n", configPath)
	fmt.Fprintf(w, "DEFAULT  ALIAS                   PROVIDER      MODEL\n")
	for _, r := range rows {
		mark := " "
		if r.Default {
			mark = "*"
		}
		fmt.Fprintf(w, "%s        %-22s %-13s %s\n", mark, r.Alias, r.Provider, r.Model)
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no aliases)")
	}
}
