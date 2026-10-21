// Package ops はウィザード(対話UI)と隠しCLIが共有するコア操作(2階建て構成の「1階」)。
//
// 設計: docs/design-distribution.md §7
//   - models.yaml の編集(プロバイダー追加・エイリアス操作) — YAMLノード編集でコメントを保持
//   - models.json のマージ(`pi install`) — providers.gateway ブロックのみ所有
package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PiMeta は pi側 models.json に書く追加メタ(models.yaml の `pi:` ブロック由来)。
type PiMeta struct {
	ContextWindow int      `yaml:"contextWindow"`
	Input         []string `yaml:"input"`
	Reasoning     *bool    `yaml:"reasoning"`
}

// --- 共通ユーティリティ ---------------------------------------------------

// writeFileSafe は同一ディレクトリに一旦tempを書いてから rename(原子的差し替え)。
func writeFileSafe(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // rename成功後は不存在でno-op
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(path); err == nil {
			_ = os.Remove(path)
		}
	}
	return os.Rename(name, path)
}

// FileExisted は path が事前に存在したか(バックアップ判断用)。
func FileExisted(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// --- YAMLノード操作 -------------------------------------------------------

// loadYAMLDocument は path のYAMLを DocumentNode として読む。
// 未存在・空・コメントのみのファイルは空マッピングの新規ドキュメント扱い。
func loadYAMLDocument(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newMappingDocument(""), nil
		}
		return nil, err
	}
	if isEmptyYAML(string(data)) {
		return newMappingDocument(""), nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%sのYAML解析に失敗: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return newMappingDocument(""), nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: 先頭がマッピング(キー: 値)ではありません", path)
	}
	return &doc, nil
}

func isEmptyYAML(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") {
			return false
		}
	}
	return true
}

// ensureTopMapping はドキュメント直下の key マッピングを取得(無ければ新規)。
func ensureTopMapping(doc *yaml.Node, key, comment string) (*yaml.Node, error) {
	return ensureMapping(doc.Content[0], key, comment)
}

// ensureMapping は m 内の key をマッピングとして取得・作成する。
// comment は新規作成時のみ親キーに付く(既存のコメントは保持)。
func ensureMapping(m *yaml.Node, key, comment string) (*yaml.Node, error) {
	if m.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("内部エラー: マッピング以外に%qを書けません", key)
	}
	if i := indexOfKey(m, key); i >= 0 {
		v := m.Content[i+1]
		switch v.Kind {
		case yaml.MappingNode:
			return v, nil
		default:
			// スカラー等の手編集変異形はマッピングへ置き換える
			*v = yaml.Node{Kind: yaml.MappingNode}
			return v, nil
		}
	}
	kn := newKeyNode(key)
	if comment != "" {
		kn.HeadComment = comment
	}
	vn := &yaml.Node{Kind: yaml.MappingNode}
	m.Content = append(m.Content, kn, vn)
	return vn, nil
}

func indexOfKey(m *yaml.Node, key string) int {
	if m.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// ensureScalar は m 内 key を文字列スカラーとして設定(位置・コメント保持)。
func ensureScalar(m *yaml.Node, key, value string) {
	if i := indexOfKey(m, key); i >= 0 {
		v := m.Content[i+1]
		v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.ScalarNode, "!!str", value, 0, nil
		return
	}
	m.Content = append(m.Content, newKeyNode(key),
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// newKeyNode は通常の文字列キーノード(インデント整形はyaml.v3に任せる)。
func newKeyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

func newMappingDocument(headComment string) *yaml.Node {
	doc := &yaml.Node{Kind: yaml.DocumentNode}
	root := &yaml.Node{Kind: yaml.MappingNode, HeadComment: headComment}
	doc.Content = []*yaml.Node{root}
	return doc
}

// --- models.yaml 操作(メニュー/ウィザードから呼ぶコア) ---------------------

// UpsertProvider は providers.<name>.base_url を設定する(他provider・コメント不変)。
func UpsertProvider(path, name, baseURL string) error {
	doc, err := loadYAMLDocument(path)
	if err != nil {
		return err
	}
	provs, err := ensureTopMapping(doc, "providers", "# 上流プロバイダーの接続情報(非秘匿)")
	if err != nil {
		return err
	}
	vn, err := ensureMapping(provs, name, "")
	if err != nil {
		return err
	}
	ensureScalar(vn, "base_url", baseURL)
	return saveYAML(path, doc)
}

// AliasSpec はエイリアス1件の登録内容。
type AliasSpec struct {
	Alias    string
	Provider string
	Model    string
	Pi       *PiMeta // 非 nil なら `pi:` ブロックとして書く
	Default  bool    // default_model をこのaliasへ
}

// UpsertAlias は models.<alias> を登録・更新する。
func UpsertAlias(path string, spec AliasSpec) error {
	if spec.Alias == "" || spec.Provider == "" || spec.Model == "" {
		return errors.New("alias / provider / model は必須です")
	}
	doc, err := loadYAMLDocument(path)
	if err != nil {
		return err
	}
	models, err := ensureTopMapping(doc, "models", "# エイリアス → provider/実モデルの対応")
	if err != nil {
		return err
	}
	am, err := ensureMapping(models, spec.Alias, "")
	if err != nil {
		return err
	}
	ensureScalar(am, "provider", spec.Provider)
	ensureScalar(am, "model", spec.Model)
	pi := spec.Pi
	if pi != nil && pi.ContextWindow == 0 && pi.Input == nil && pi.Reasoning == nil {
		pi = nil
	}
	if pi != nil {
		pim, err := ensureMapping(am, "pi", "")
		if err != nil {
			return err
		}
		if pi.ContextWindow != 0 {
			ensureInt(pim, "contextWindow", pi.ContextWindow)
		}
		if pi.Input != nil {
			ensureStringList(pim, "input", pi.Input)
		}
		if pi.Reasoning != nil {
			ensureBool(pim, "reasoning", *pi.Reasoning)
		}
	}
	if spec.Default {
		ensureScalar(doc.Content[0], "default_model", spec.Alias)
	}
	return saveYAML(path, doc)
}

// DeleteAlias は models.<alias> を削除する。最後の1つは拒否する。
// 削除対象が default_model だった場合は残りから1つを昇格する。
func DeleteAlias(path, alias string) error {
	doc, err := loadYAMLDocument(path)
	if err != nil {
		return err
	}
	root := doc.Content[0]
	i := indexOfKey(root, "models")
	if i < 0 {
		return errors.New("models セクションがありません")
	}
	ms := root.Content[i+1]
	if ms.Kind != yaml.MappingNode {
		return errors.New("models セクションがマッピングではありません")
	}
	idx := indexOfKey(ms, alias)
	if idx < 0 {
		return fmt.Errorf("エイリアス %q がありません", alias)
	}
	if len(ms.Content)/2 <= 1 {
		return errors.New("最後のエイリアスは削除できません(先に別のエイリアスを追加してください)")
	}
	ms.Content = append(ms.Content[:idx], ms.Content[idx+2:]...)
	if dm := indexOfKey(root, "default_model"); dm >= 0 && root.Content[dm+1].Value == alias {
		ensureScalar(root, "default_model", firstAlias(ms))
	}
	return saveYAML(path, doc)
}

// SetDefaultModel は default_model を alias に変更する。
func SetDefaultModel(path, alias string) error {
	doc, err := loadYAMLDocument(path)
	if err != nil {
		return err
	}
	root := doc.Content[0]
	i := indexOfKey(root, "models")
	if i < 0 {
		return errors.New("models セクションがありません")
	}
	if indexOfKey(root.Content[i+1], alias) < 0 {
		return fmt.Errorf("エイリアス %q がありません", alias)
	}
	ensureScalar(root, "default_model", alias)
	return saveYAML(path, doc)
}

func firstAlias(ms *yaml.Node) string {
	if len(ms.Content) >= 2 {
		return ms.Content[0].Value
	}
	return ""
}

// WriteInitialConfig は初期 config/models.yaml を生成する(コメント付き)。
func WriteInitialConfig(path string, providers []ProviderSpec, specs []AliasSpec) error {
	doc := newMappingDocument(
		"# pi-llm-gateway config (非秘匿)。実キーは secrets/secrets.yaml.age で管理。\n" +
			"# 左側のエイリアスが pi側(/model)から選べる id として列挙されます(GET /v1/models)。")
	root := doc.Content[0]
	if len(specs) > 0 {
		ensureScalar(root, "default_model", specs[0].Alias)
	}
	modelsm, err := ensureMapping(root, "models", "")
	if err != nil {
		return err
	}
	for _, s := range specs {
		if err := UpsertAliasIn(modelsm, s); err != nil {
			return err
		}
	}
	provs, err := ensureMapping(root, "providers", "")
	if err != nil {
		return err
	}
	for _, p := range providers {
		pm, err := ensureMapping(provs, p.ID, "")
		if err != nil {
			return err
		}
		ensureScalar(pm, "base_url", p.BaseURL)
	}
	return saveYAML(path, doc)
}

// AliasForPi などエイリアス共通形。AliasSpec はファイルに書く内容そのもの。
// ProviderSpec は providers セクションに書く内容。
type ProviderSpec struct {
	ID      string
	BaseURL string
}

// UpsertAliasIn は既存 models マッピングノードへ spec を書く(WriteInitialConfig内部用)。
func UpsertAliasIn(models *yaml.Node, spec AliasSpec) error {
	am, err := ensureMapping(models, spec.Alias, "")
	if err != nil {
		return err
	}
	ensureScalar(am, "provider", spec.Provider)
	ensureScalar(am, "model", spec.Model)
	if spec.Pi != nil {
		pim, err := ensureMapping(am, "pi", "")
		if err != nil {
			return err
		}
		if spec.Pi.ContextWindow != 0 {
			ensureInt(pim, "contextWindow", spec.Pi.ContextWindow)
		}
		if spec.Pi.Input != nil {
			ensureStringList(pim, "input", spec.Pi.Input)
		}
		if spec.Pi.Reasoning != nil {
			ensureBool(pim, "reasoning", *spec.Pi.Reasoning)
		}
	}
	return nil
}

// --- ノード書き込みヘルパ ---------------------------------------------------

func ensureInt(m *yaml.Node, key string, v int) {
	ensureRaw(m, key, func(n *yaml.Node) {
		n.Kind, n.Tag, n.Value, n.Style, n.Content = yaml.ScalarNode, "!!int", fmt.Sprint(v), 0, nil
	})
}

func ensureBool(m *yaml.Node, key string, v bool) {
	s := "false"
	if v {
		s = "true"
	}
	ensureRaw(m, key, func(n *yaml.Node) {
		n.Kind, n.Tag, n.Value, n.Style, n.Content = yaml.ScalarNode, "!!bool", s, 0, nil
	})
}

func ensureStringList(m *yaml.Node, key string, items []string) {
	ensureRaw(m, key, func(n *yaml.Node) {
		n.Kind, n.Tag, n.Value, n.Style = yaml.SequenceNode, "!!seq", "", 0
		n.Content = n.Content[:0]
		for _, it := range items {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: it})
		}
	})
}

func ensureRaw(m *yaml.Node, key string, mk func(*yaml.Node)) {
	if i := indexOfKey(m, key); i >= 0 {
		mk(m.Content[i+1])
		return
	}
	vn := &yaml.Node{Kind: yaml.ScalarNode}
	mk(vn)
	m.Content = append(m.Content, newKeyNode(key), vn)
}

func saveYAML(path string, doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%sのYAML生成に失敗: %w", path, err)
	}
	return writeFileSafe(path, out, 0o644)
}

// --- models.json マージ ----------------------------------------------------

// PiCatalogMeta はエイリアス→piメタの解決結果を pi install に渡すための注入点。
// カタログ既定(builtin)と models.yaml `pi:` 上書き、両方を合成して呼び出し側が作る。
type PiCatalogMeta struct {
	ContextWindow int
	Input         []string
	Reasoning     bool
}

// AliasForPi は models.yaml の1エイリアス+解決済みpiメタ。
type AliasForPi struct {
	Alias    string
	Provider string
	Model    string
	Meta     PiCatalogMeta
}

// PiInstallOptions は `pi install` コアの入力。
type PiInstallOptions struct {
	ModelsPath string // ~/.pi/agent/models.json
	BaseURL    string // 例: http://127.0.0.1:18080/v1
	Aliases    []AliasForPi
	// GatewayKey は書き込むべきgatewayキー(呼び出し側がsecretsに確保済みのもの)。
	// models.json に gk- キーがあり secrets と一致するならそちらが優先される(流用)。
	GatewayKey string
	// HasGatewayKey は既存キーがsecretsに実在するかを尋ねる。
	HasGatewayKey func(key string) bool
	// NewGatewayKey は GatewayKey 未指定かつ既存キーが無い時に生成するfallback。
	// 対話メニュー経路(生成→secrets保存→書き込み)のためだけに用意する。
	NewGatewayKey func() (string, error)
	// ForceNewKey は secrets に無い既存 gk- キーを確認なしに潰す(非対話 --pi-key-replace)。
	ForceNewKey bool
	// Confirm は対話確認([y/N])。非対話では nil。
	Confirm func(message string) bool
	Quiet   bool
}

// PiInstallResult は実行結果(サマリ表示に使う)。
type PiInstallResult struct {
	GatewayKey  string
	ReusedKey   bool
	BackupPath  string // 新規作成時は空
	Changed     bool
	ModelsCount int
}

// PiInstall は pi の models.json に providers.gateway ブロックをマージする。
//
// 規約(design-distribution.md §4):
//   - 他プロバイダー・他トップレベルキーは一切変更しない
//   - 既存 apiKey が gk- 接頭かつ HasGatewayKey==true なら流用
//   - 書き込み前にタイムスタンプ付きバックアップを作り3世代まで残す
//
// 既存キーを差し替えた際のsecrets保存は呼び出し側(NewGatewayKey実装)が行う。
func PiInstall(opts PiInstallOptions) (PiInstallResult, error) {
	if len(opts.Aliases) == 0 {
		return PiInstallResult{}, errors.New("エイリアスがありません(config/models.yamlを確認してください)")
	}
	existing, models, err := loadPiModels(opts.ModelsPath)
	if err != nil {
		return PiInstallResult{}, err
	}
	prev := ""
	if existing != nil {
		if provs, ok := existing["providers"].(map[string]any); ok {
			if gb, ok := provs["gateway"].(map[string]any); ok {
				if k, _ := gb["apiKey"].(string); strings.HasPrefix(k, "gk-") {
					prev = k
				}
			}
		}
	}
	key := prev
	reused := prev != ""
	switch {
	case prev != "" && opts.HasGatewayKey != nil && opts.HasGatewayKey(prev):
		// secrets一致 → 流用(何もしない)
	case prev != "":
		// secrets に無い手入力キーの可能性 → 確認無しには潰さない
		if !opts.ForceNewKey {
			msg := fmt.Sprintf("pi models.json に secrets に存在しない gatewayキー(%s… )が設定されています。新しく生成したキーで置き換えますか?", fp(prev))
			if opts.Confirm != nil && opts.Confirm(msg) {
				// 下へ流れて再生成
			} else {
				return PiInstallResult{}, fmt.Errorf("既存の gatewayキーが secrets に存在しません。手入力キーを維持するには鍵を secrets に追加してください(`gateway keys add %s…` 相当、または再セットアップ --pi-key-replace)", fp(prev))
			}
		}
		if opts.NewGatewayKey == nil {
			return PiInstallResult{}, errors.New("gatewayキー生成器がありません")
		}
		k, err := opts.NewGatewayKey()
		if err != nil {
			return PiInstallResult{}, err
		}
		key = k
		reused = false
	default:
		if opts.GatewayKey != "" {
			// setup本体が確保済みのキーを使う(正常系)
			key = opts.GatewayKey
			reused = false
			break
		}
		if opts.NewGatewayKey == nil {
			return PiInstallResult{}, errors.New("gatewayキーがありません(setupを先に実行してください)")
		}
		k, err := opts.NewGatewayKey()
		if err != nil {
			return PiInstallResult{}, err
		}
		key = k
		reused = false
	}

	// マージ: gateway ブロックのみ差し替え
	entry := map[string]any{
		"api":      "openai-completions",
		"apiKey":   key,
		baseUrlKey: opts.BaseURL,
		"models":   piModelsArray(opts.Aliases),
	}
	provIf := models["providers"]
	prov, ok := provIf.(map[string]any)
	if !ok {
		prov = map[string]any{}
		models["providers"] = prov
	}
	prov["gateway"] = entry

	out, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		return PiInstallResult{}, err
	}
	out = append(out, '\n')

	backup, err := backupPiModels(opts.ModelsPath)
	if err != nil {
		return PiInstallResult{}, err
	}
	if err := writeFileSafe(opts.ModelsPath, out, 0o644); err != nil {
		return PiInstallResult{}, err
	}
	return PiInstallResult{GatewayKey: key, ReusedKey: reused, BackupPath: backup,
		Changed: true, ModelsCount: len(opts.Aliases)}, nil
}

const baseUrlKey = "baseUrl"

func piModelsArray(aliases []AliasForPi) []map[string]any {
	arr := make([]map[string]any, 0, len(aliases))
	for _, a := range aliases {
		m := map[string]any{
			"id":      a.Alias,
			"_launch": true,
		}
		if a.Meta.ContextWindow != 0 {
			m["contextWindow"] = a.Meta.ContextWindow
		}
		if len(a.Meta.Input) > 0 {
			in := append([]string(nil), a.Meta.Input...)
			m["input"] = in
		}
		m["reasoning"] = a.Meta.Reasoning
		arr = append(arr, m)
	}
	return arr
}

// loadPiModels は models.json を読む。未存在なら新規素体を返す。
func loadPiModels(path string) (map[string]any, map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			models := map[string]any{"providers": map[string]any{}}
			return nil, models, nil
		}
		return nil, nil, fmt.Errorf("%sを読めません: %w", path, err)
	}
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, fmt.Errorf("%sのJSON解析に失敗: %w", path, err)
	}
	if top == nil {
		top = map[string]any{}
	}
	return top, top, nil
}

// backupPiModels は既存 models.json をタイムスタンプ付きで退避し、3世代に整理する。
func backupPiModels(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil // 新規作成: バックアップ不要
		}
		return "", err
	}
	ts := time.Now().Format("20060102-150405")
	bak := path + ".bak-" + ts
	if err := os.WriteFile(bak, data, 0o644); err != nil {
		return "", fmt.Errorf("バックアップ作成に失敗(%s): %w", bak, err)
	}
	pruneBackups(path)
	return bak, nil
}

// pruneBackups は models.json.bak-YYYYMMDD-hhmmss を新しい順に3世代だけ残す。
func pruneBackups(path string) {
	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) <= 3 {
		return
	}
	// 名前順=タイムスタンプ順(固定書式のため辞書順は時系列)
	sortStringsDESC(matches)
	for _, m := range matches[3:] {
		_ = os.Remove(m)
	}
}

func sortStringsDESC(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] > s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func fp(key string) string {
	if len(key) <= 4 {
		return "…"
	}
	return key[:4]
}

// --- pi uninstall -----------------------------------------------------------

// PiUninstall は providers.gateway ブロックのみ削除する(他は不変)。
func PiUninstall(modelsPath string) error {
	_, models, err := loadPiModels(modelsPath)
	if err != nil {
		return err
	}
	provIf := models["providers"]
	prov, ok := provIf.(map[string]any)
	if !ok {
		return errors.New("providers セクションがありません(既に解除済み)")
	}
	if _, has := prov["gateway"]; !has {
		return errors.New("gateway プロバイダーがmodels.jsonにありません(既に解除済み)")
	}
	delete(prov, "gateway")
	out, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	// 解除時もバックアップを残す(復元可能)
	if _, err := backupPiModels(modelsPath); err != nil {
		return err
	}
	return writeFileSafe(modelsPath, out, 0o644)
}
