package wizard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"gopkg.in/yaml.v3"

	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/ops"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
	"github.com/trork-code/pi-llm-gateway/internal/setup"
)

// Options は init 実行の入力(対話/非対話の共通形)。
type Options struct {
	RuntimeDir        string // 空なら既定(~/.pi-gateway)
	Provider          string // catalog id
	APIKeySpec        string // env:/file:-/値
	Model             string
	Alias             string
	Pi                bool   // pi models.json に登録する
	Startup           string // manual | resident(バッチ3)
	Port              string
	Bind              string
	Yes               bool // 非対話
	PrintJSON         bool
	Quiet             bool
	ForcePiKeyReplace bool // secretsに無い既存gk-キーの置換を許可(非対話の明示意志)
	Stdin             io.Reader
}

// Summary は完了出力(対話サマリ/PrintJSON の共通形)。
type Summary struct {
	RuntimeDir   string   `json:"runtime_dir"`
	Provider     string   `json:"provider"`
	Alias        string   `json:"alias"`
	Model        string   `json:"model"`
	BaseURL      string   `json:"base_url"`
	Port         string   `json:"port"`
	Startup      string   `json:"startup"`
	IdentityMode string   `json:"identity_mode"`
	PiRegistered bool     `json:"pi_registered"`
	PiModels     int      `json:"pi_models"`
	GatewayKeyFP string   `json:"gateway_key_fp"`
	ProviderFP   string   `json:"provider_key_fp"`
	Backups      []string `json:"pi_backups"`
	Verified     string   `json:"verified"` // "ok" / "skipped(<理由>)"
}

// ErrAborted は対話中断(Ctrl+C含む)で正常に抜ける合図。
var ErrAborted = errors.New("setup aborted (nothing further was changed)")

// RunSetup はセットアップ本体。対話/非対話は事前に Options を組み立てて呼ぶ。
func RunSetup(app Options, stdout io.Writer) (*Summary, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	dir, err := setup.ResolveDir(app.RuntimeDir)
	if err != nil {
		return nil, err
	}
	paths := setup.Layout(dir)
	if app.Provider == "" {
		return nil, errors.New("provider が未指定です")
	}
	prov, ok := Look(app.Provider)
	if !ok || prov.Disabled {
		return nil, fmt.Errorf("未対応の provider %q (対応: %v)", app.Provider, EnabledIDs())
	}
	if app.Alias == "" {
		app.Alias = defaultAliasName(app.Provider)
	}
	if err := validAlias(app.Alias); err != nil {
		return nil, err
	}
	if app.Model == "" {
		app.Model = prov.Suggest
	}
	if app.Model == "" {
		return nil, fmt.Errorf("provider %q は推奨モデルを持たないため --model が必須です", app.Provider)
	}
	if app.Startup == "" {
		app.Startup = "manual"
	}
	if app.Startup != "manual" && app.Startup != "resident" {
		return nil, fmt.Errorf("startup は manual|resident (現在: %q)。resident は自動起動整備後のリリースで有効化", app.Startup)
	}
	if app.Startup == "resident" {
		// バッチ1ではランチャまで生成し、自動起動登録はバッチ3(設計§9)。案内だけ出して継続
		if !app.Quiet {
			fmt.Fprintln(stdout, "note: login auto-start lands in a later release; writing the manual launcher for now.")
		}
		app.Startup = "manual"
	}
	if app.Port == "" {
		app.Port = "18080" // ウィザード既定(設計§5.1)
	}
	if app.Bind == "" {
		app.Bind = "127.0.0.1"
	}
	spec := app.APIKeySpec
	if spec == "" {
		return nil, errors.New("API key が未指定です")
	}
	apiKey, err := ResolveAPIKeySource(spec, app.Stdin)
	if err != nil {
		return nil, err
	}

	// 1. runtime dir + 自己コピー + ランチャ
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		return nil, err
	}
	if note, err := setup.SelfCopy(paths); err != nil {
		return nil, err
	} else if note != "" && !app.Quiet {
		fmt.Fprintln(stdout, "note: "+note)
	}

	// 2. identity(バッチ1は file モードのみ。既存 identity があれば再利用)
	identityMode := "file"
	recipients, err := ensureIdentity(paths)
	if err != nil {
		return nil, err
	}

	// 3. secrets(既存在すれば読み込んで upsert、無ければ新規)
	sec, existed, err := loadOrCreateSecrets(paths)
	if err != nil {
		return nil, err
	}
	gwKey := ""
	if keys := sec.AllGatewayKeys(); len(keys) > 0 {
		gwKey = keys[0] // 既存キーの保全(設計§4.3 / §4.3の流用規則)
	} else {
		gwKey, err = GenerateGatewayKey()
		if err != nil {
			return nil, err
		}
		sec.GatewayKeys = appendUnique(sec.GatewayKeys, gwKey)
	}
	if sec.APIKeys == nil {
		sec.APIKeys = map[string]string{}
	}
	sec.APIKeys[app.Provider] = apiKey
	if err := secrets.Save(paths.Secrets, sec, recipients); err != nil {
		return nil, fmt.Errorf("secretsの暗号化に失敗: %w", err)
	}
	_ = existed

	// 4. config/models.yaml(未存在なら初期生成、在れば upsert)
	createdConfig := !ops.FileExisted(paths.Config)
	specs := []ops.AliasSpec{{Alias: app.Alias, Provider: app.Provider, Model: app.Model,
		Pi: nil, Default: true}}
	provs := []ops.ProviderSpec{{ID: app.Provider, BaseURL: prov.BaseURL}}
	if createdConfig {
		if err := ops.WriteInitialConfig(paths.Config, provs, specs); err != nil {
			return nil, err
		}
	} else {
		// piメタはカタログ既定→models.yamlは手編集域のため、ここでは書かない(§3注記)
		if err := ops.UpsertProvider(paths.Config, app.Provider, prov.BaseURL); err != nil {
			return nil, err
		}
		if err := ops.UpsertAlias(paths.Config, ops.AliasSpec{Alias: app.Alias,
			Provider: app.Provider, Model: app.Model}); err != nil {
			return nil, err
		}
		if !hasDefaultModel(paths.Config) {
			if err := ops.SetDefaultModel(paths.Config, app.Alias); err != nil {
				return nil, err
			}
		}
	}

	// 5. pi登録(既定ON)
	piRes, piBackups := ops.PiInstallResult{}, []string(nil)
	if app.Pi {
		aliases, err := aliasesForPi(paths.Config, app.Provider)
		if err != nil {
			return nil, err
		}
		loaded := sec
		piRes, err = ops.PiInstall(ops.PiInstallOptions{
			ModelsPath: piModelsPath(),
			BaseURL:    fmt.Sprintf("http://127.0.0.1:%s/v1", app.Port),
			Aliases:    aliases,
			GatewayKey: gwKey,
			HasGatewayKey: func(k string) bool {
				for _, kk := range loaded.AllGatewayKeys() {
					if kk == k {
						return true
					}
				}
				return false
			},
			ForceNewKey: app.ForcePiKeyReplace,
		})
		if err != nil {
			return nil, fmt.Errorf("pi models.json への登録に失敗: %w", err)
		}
		piBackups = listPiBackups()
		_ = piRes
	}

	// 6. ランチャ + setup.json
	if err := setup.WriteLauncher(paths, setup.LauncherInput{
		Port: app.Port, Bind: app.Bind, Log: paths.Log,
		IdentityMode: "file",
	}); err != nil {
		return nil, err
	}
	cfgProviders, _ := configProviders(paths.Config)
	st := &setup.State{
		Startup:      "manual",
		IdentityMode: identityMode,
		Port:         atoi(app.Port),
		Bind:         app.Bind,
		Providers:    cfgProviders,
		Aliases:      configAliases(paths.Config),
		DefaultModel: app.Alias,
		PiRegistered: app.Pi,
	}
	if prev, err := setup.LoadState(paths); err == nil && prev != nil {
		st.CreatedAt = prev.CreatedAt
		st.Version = prev.Version
	}
	if err := setup.SaveState(paths, st); err != nil {
		return nil, err
	}

	// 7. 上流キーの軽検証(失敗は警告のみ・非致命)
	verified := verifyUpstream(prov, apiKey, stdout, app.Quiet)

	sum := &Summary{
		RuntimeDir:   dir,
		Provider:     app.Provider,
		Alias:        app.Alias,
		Model:        app.Model,
		BaseURL:      fmt.Sprintf("http://127.0.0.1:%s/v1", app.Port),
		Port:         app.Port,
		Startup:      "manual",
		IdentityMode: identityMode,
		PiRegistered: app.Pi,
		GatewayKeyFP: fp(gwKey),
		ProviderFP:   fp(apiKey),
		Backups:      piBackups,
		Verified:     verified,
	}
	if app.Pi {
		sum.PiModels = len(configAliases(paths.Config))
	}
	if app.PrintJSON {
		out, err := json.MarshalIndent(sum, "", "  ")
		if err != nil {
			return sum, err
		}
		fmt.Fprintln(stdout, string(out))
	}
	return sum, nil
}

// --- helpers ---------------------------------------------------------------

func defaultAliasName(provider string) string { return "pi-default" }

func validAlias(a string) error {
	if a == "" || len(a) > 64 {
		return errors.New("alias は 1..64 文字で指定してください")
	}
	for _, r := range a {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("alias に使えるのは英数字と - _ . です(違反: %q)", string(r))
		}
	}
	return nil
}

// ensureIdentity は runtime secrets/identity.txt を確保し、recipient 一覧を返す。
func ensureIdentity(paths setup.Paths) ([]string, error) {
	if data, err := os.ReadFile(paths.Identity); err == nil {
		id, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("既存identity(%s)を解釈できません: %w", paths.Identity, err)
		}
		return []string{id.Recipient().String()}, nil
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("identity生成に失敗: %w", err)
	}
	if err := os.WriteFile(paths.Identity, []byte(id.String()+"\n"), 0o600); err != nil {
		return nil, err
	}
	return []string{id.Recipient().String()}, nil
}

func loadOrCreateSecrets(paths setup.Paths) (*secrets.Secrets, bool, error) {
	data, err := os.ReadFile(paths.Identity)
	if err != nil {
		return nil, false, err
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, false, err
	}
	if _, stat := os.Stat(paths.Secrets); stat != nil {
		return &secrets.Secrets{}, false, nil
	}
	sec, err := secrets.Load(secrets.LoadOptions{
		EncryptedPath: paths.Secrets,
		Identity:      strings.NewReader(id.String()),
	})
	if err != nil {
		return nil, false, fmt.Errorf("既存secrets(%s)の復号に失敗: %w", paths.Secrets, err)
	}
	return sec, true, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// yamlAliasFile は models.yaml のエイリアス(piメタ込み)を読む読み取り専用構造。
type yamlAliasFile struct {
	DefaultModel string                    `yaml:"default_model"`
	Models       map[string]yamlAliasEntry `yaml:"models"`
}

type yamlAliasEntry struct {
	Provider string      `yaml:"provider"`
	Model    string      `yaml:"model"`
	Pi       *ops.PiMeta `yaml:"pi"`
}

func readYAMLAliases(path string) (*yamlAliasFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f yamlAliasFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// AliasesForPi は config エイリアスを pi models.json 配列へ合成する(隠しCLI用)。
// メタはカタログ既定 → models.yaml `pi:` 上書き(design §4.4)。
func AliasesForPi(configPath string) ([]ops.AliasForPi, error) { return aliasesForPi(configPath, "") }

// piModelsPath / Fingerprint は隠しCLI・doctor 表示向けのエクスポート。
func PiModelsPath() string { return piModelsPath() }

// Fingerprint は鍵表示用先頭4文字(+…)。
func Fingerprint(key string) string { return fp(key) }

// aliasesForPi は config エイリアスを pi models.json 配列へ合成する。
// メタはカタログ既定 → models.yaml `pi:` 上書き(design §4.4)。
func aliasesForPi(configPath, preferProvider string) ([]ops.AliasForPi, error) {
	f, err := readYAMLAliases(configPath)
	if err != nil {
		return nil, err
	}
	// 順序はエイリアス名順(決定的出力)
	names := make([]string, 0, len(f.Models))
	for name := range f.Models {
		names = append(names, name)
	}
	sortStrings(names)
	out := make([]ops.AliasForPi, 0, len(names))
	for _, name := range names {
		e := f.Models[name]
		p, ok := Look(e.Provider)
		cw, input, reasoning := 0, []string(nil), false
		if ok {
			cw, input, reasoning = p.CW, p.Input, p.Reasoning
		}
		if e.Pi != nil {
			if e.Pi.ContextWindow != 0 {
				cw = e.Pi.ContextWindow
			}
			if e.Pi.Input != nil {
				input = e.Pi.Input
			}
			if e.Pi.Reasoning != nil {
				reasoning = *e.Pi.Reasoning
			}
		}
		out = append(out, ops.AliasForPi{
			Alias: name, Provider: e.Provider, Model: e.Model,
			Meta: ops.PiCatalogMeta{ContextWindow: cw, Input: input, Reasoning: reasoning},
		})
	}
	return out, nil
}

func hasDefaultModel(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "default_model:") {
			return true
		}
	}
	return false
}

func configAliases(path string) []string {
	f, err := readYAMLAliases(path)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(f.Models))
	for name := range f.Models {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func configProviders(path string) ([]string, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sortStrings(names)
	return names, nil
}

func piModelsPath() string {
	// テスト/E2Eフック: 既定は ~/.pi/agent/models.json
	if v := os.Getenv("GATEWAY_PI_MODELS"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".pi", "agent", "models.json")
	}
	return filepath.Join(home, ".pi", "agent", "models.json")
}

func listPiBackups() []string {
	matches, _ := filepath.Glob(piModelsPath() + ".bak-*")
	sortStrings(matches)
	return matches
}

// verifyUpstream は最小リクエスト(GET <base_url>/models)で鍵の当たりを確認する。
// ネットワーク障害・未対応エンドポイントは非致命(skipped)として扱う。
func verifyUpstream(prov Provider, apiKey string, stdout io.Writer, quiet bool) string {
	if quiet {
		return "skipped"
	}
	ok, err := probeUpstream(prov.BaseURL, apiKey)
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "key check skipped (%v) — you can adjust later with `gateway keys set %s <KEY>`.\n", err, prov.ID)
		return "skipped(" + err.Error() + ")"
	case ok:
		fmt.Fprintf(stdout, "key check: OK (%s accepted the key)\n", prov.ID)
		return "ok"
	default:
		fmt.Fprintf(stdout, "WARNING: %s rejected the key (upstream 401). Fix with `gateway keys set %s <KEY>` or re-run `gateway`.\n", prov.ID, prov.ID)
		return "upstream rejected the key (401)"
	}
}

// probeUpstream は /v1/models を叩き 200 か判定する(3秒タイムアウト)。
func probeUpstream(baseURL, apiKey string) (bool, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
