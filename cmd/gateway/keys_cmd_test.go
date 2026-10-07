// gateway keys サブコマンドのテスト。
// 実環境に依存せず、テスト用のidentity/secretsファイルを一時ディレクトリに作って検証する。
package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"gopkg.in/yaml.v3"

	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

// discardLog はテスト中のログを無効化する。
func discardLog(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeTestSecrets はsecをage暗号化(armor)してdir配下に書き出し、パスを返す。
func writeTestSecrets(t *testing.T, dir string, id *age.X25519Identity, sec *secrets.Secrets) string {
	t.Helper()
	plain, err := yaml.Marshal(sec)
	if err != nil {
		t.Fatalf("YAMLの生成: %v", err)
	}
	var buf strings.Builder
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, id.Recipient())
	if err != nil {
		t.Fatalf("age暗号化: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("暗号化書き込み: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("age close: %v", err)
	}
	if err := aw.Close(); err != nil {
		t.Fatalf("armor close: %v", err)
	}
	p := filepath.Join(dir, "secrets.yaml.age")
	if err := os.WriteFile(p, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("secretsの書き出し: %v", err)
	}
	return p
}

// stringWriter は不要になったため廃止(文字列.Builderはio.Writerを直接満たす)。

// keysTestEnv はテスト用のidentityファイルと暗号化secretsを環境変数に結び付ける。
// 戻り値はsecretsファイルのパス。
func keysTestEnv(t *testing.T, sec *secrets.Secrets) string {
	t.Helper()
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("identityの生成: %v", err)
	}
	identFile := filepath.Join(dir, "identity.txt")
	if err := os.WriteFile(identFile, []byte(id.String()), 0o600); err != nil {
		t.Fatalf("identityファイルの作成: %v", err)
	}
	if err := os.Chmod(identFile, 0o600); err != nil { // unix CIでも owner-only(Windowsでは実質no-op)
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv("AGE_IDENTITY_FILE", identFile)
	t.Setenv("AGE_IDENTITY", "")
	t.Setenv("AGE_IDENTITY_CMD", "")
	t.Setenv("AGE_PASSPHRASE", "")
	t.Setenv("SECRETS_DECRYPT_CMD", "")

	if sec == nil {
		sec = &secrets.Secrets{Version: 1, GatewayKey: "gk-base-0001"}
	}
	t.Setenv("SECRETS_FILE", writeTestSecrets(t, dir, id, sec))
	return os.Getenv("SECRETS_FILE")
}

// loadFromDisk は現在のSECRETS_FILEを復号して確認用に返す。
func loadFromDisk(t *testing.T) *secrets.Secrets {
	t.Helper()
	idFile := os.Getenv("AGE_IDENTITY_FILE")
	data, err := os.ReadFile(idFile)
	if err != nil {
		t.Fatalf("identity読み取り: %v", err)
	}
	sec, err := secrets.Load(secrets.LoadOptions{
		EncryptedPath: os.Getenv("SECRETS_FILE"),
		Identity:      strings.NewReader(string(data)),
	})
	if err != nil {
		t.Fatalf("再読み込み: %v", err)
	}
	return sec
}

// runCmd はrunKeysOpの薄いラッパ。
func runCmd(t *testing.T, op string, kf *keyFlags, args []string, stdin string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := runKeysOp(discardLog(t), op, kf, args, strings.NewReader(stdin), &out)
	return out.String(), err
}

// TestKeysAddGeneratesRandomKey は引数なしaddでランダムキーが生成・保存される。
func TestKeysAddGeneratesRandomKey(t *testing.T) {
	keysTestEnv(t, nil)
	out, err := runCmd(t, "add", &keyFlags{addr: resolveGatewayURL("")}, nil, "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "gk-") {
		t.Errorf("生成キー表示がない: %s", out)
	}
	if got := loadFromDisk(t).AllGatewayKeys(); len(got) != 2 {
		t.Errorf("keys count = %d, want 2: %q", len(got), got)
	}
}

// TestKeysAddExplicitValue は値指定の追加を確認する。
func TestKeysAddExplicitValue(t *testing.T) {
	keysTestEnv(t, nil)
	out, err := runCmd(t, "add", nil, []string{"gk-mytoken"}, "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "追加しました") {
		t.Errorf("出力: %s", out)
	}
	if got := loadFromDisk(t).AllGatewayKeys(); !containsStr(got, "gk-mytoken") {
		t.Errorf("追加したキーが保存されていない: %q", got)
	}
}

// TestKeysAddDuplicateIsNoop は重複追加が変更なしで終わることを確認する。
func TestKeysAddDuplicateIsNoOp(t *testing.T) {
	keysTestEnv(t, &secrets.Secrets{Version: 1, GatewayKey: "gk-existing-1"})
	out, err := runCmd(t, "add", nil, []string{"gk-existing-1"}, "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "既に登録") {
		t.Errorf("出力: %s", out)
	}
	if got := loadFromDisk(t).AllGatewayKeys(); len(got) != 1 {
		t.Errorf("重複複製された: %q", got)
	}
}

// TestKeysRemoveLockoutGuard は最後のgatewayキーの削除を拒否する。
func TestKeysRemoveLockoutGuard(t *testing.T) {
	path := keysTestEnv(t, nil)
	if _, err := runCmd(t, "remove", nil, []string{"gk-base-0001"}, ""); err == nil {
		t.Fatal("ロックアウトの拒否が効いていない")
	} else if !strings.Contains(err.Error(), "ロックアウト") {
		t.Errorf("期待メッセージなし: %v", err)
	}
	if got := loadFromDisk(t).AllGatewayKeys(); len(got) != 1 || got[0] != "gk-base-0001" {
		t.Errorf("拒否済みのキーが削除された: %q", got)
	}
	_ = path
}

// TestKeysRemovePrefixUnique はユニーク前方一致での削除を確認する。
func TestKeysRemovePrefixUnique(t *testing.T) {
	keysTestEnv(t, &secrets.Secrets{Version: 1, GatewayKeys: []string{"gk-alpha-1", "gk-beta-1"}})
	out, err := runCmd(t, "remove", nil, []string{"gk-alp"}, "")
	if err != nil {
		t.Fatalf("remove: %v\n出力: %s", err, out)
	}
	got := loadFromDisk(t).AllGatewayKeys()
	if len(got) != 1 || got[0] != "gk-beta-1" {
		t.Errorf("keys = %q, want [gk-beta-1]", got)
	}
}

// TestKeysRemoveAmbiguousPrefix は曖昧な前方一致を拒否する。
func TestKeysRemoveAmbiguousPrefix(t *testing.T) {
	keysTestEnv(t, &secrets.Secrets{Version: 1, GatewayKeys: []string{"gk-aaa-1", "gk-aab-2", "gk-zz-1"}})
	_, err := runCmd(t, "remove", nil, []string{"gk-aa"}, "")
	if err == nil || !strings.Contains(err.Error(), "曖昧") {
		t.Errorf("曖昧の拒否がされていない: %v", err)
	}
}

// TestKeysSetFromStdin は set の標準入力経路を確認する。
func TestKeysSetFromStdin(t *testing.T) {
	keysTestEnv(t, nil)
	out, err := runCmd(t, "set", &keyFlags{addr: resolveGatewayURL("")}, []string{"ollamacloud", "-"}, "sk-live-abc\n")
	if err != nil {
		t.Fatalf("set: %v\n出力: %s", err, out)
	}
	sec := loadFromDisk(t)
	if sec.APIKeys["ollamacloud"] != "sk-live-abc" {
		t.Errorf("APIKeys = %v", sec.APIKeys)
	}
}

// TestKeysSetNoChangeIsNoOp は同一値のsetが変更なしで終わることを確認する。
func TestKeysSetNoChangeIsNoOp(t *testing.T) {
	keysTestEnv(t, &secrets.Secrets{Version: 1, GatewayKey: "k", APIKeys: map[string]string{"openai": "sk-same"}})
	out, err := runCmd(t, "set", nil, []string{"openai"}, "sk-same")
	if err != nil || !strings.Contains(out, "変更なし") {
		t.Errorf("out=%s err=%v", out, err)
	}
	// stdinが空(値省略)の場合はエラー
	if _, err := runCmd(t, "set", nil, []string{"openai"}, ""); err == nil {
		t.Errorf("空キーのsetが通ってしまった")
	}
	if sec := loadFromDisk(t); sec.APIKeys["openai"] != "sk-same" {
		t.Errorf("APIKeys = %v", sec.APIKeys)
	}
}

// TestKeysUnsetMissingIsNoOp は存在しないproviderのunsetが変更なしで終わることを確認。
func TestKeysUnsetMissingIsNoOp(t *testing.T) {
	keysTestEnv(t, nil)
	out, err := runCmd(t, "unset", nil, []string{"nope-provider"}, "")
	if err != nil || !strings.Contains(out, "設定がありません") {
		t.Errorf("out=%s err=%v", out, err)
	}
	// 保存済みファイルが書き換わっていないこと(mtimeではなく内容で確認)
	if got := loadFromDisk(t); got.APIKeys["nope-provider"] != "" {
		t.Errorf("予期しない追加: %v", got.APIKeys)
	}
}

// TestKeysUnsetRemovesProvider は providerキーの削除を確認する。
func TestKeysUnsetRemovesProvider(t *testing.T) {
	keysTestEnv(t, &secrets.Secrets{Version: 1, GatewayKey: "k", APIKeys: map[string]string{"groq": "sk-x"}})
	if _, err := runCmd(t, "unset", nil, []string{"groq"}, ""); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if sec := loadFromDisk(t); func() bool { _, ok := sec.APIKeys["groq"]; return ok }() {
		t.Errorf("api_keys.groq が残っている: %v", sec.APIKeys)
	}
}

// TestKeysListHidesValues はlistが値そのものを出力しないことを確認する。
func TestKeysListHidesValues(t *testing.T) {
	keysTestEnv(t, &secrets.Secrets{
		Version:    1,
		GatewayKey: "secret-gateway-value-1234",
		APIKeys:    map[string]string{"openai": "sk-full-secret-9999"},
	})
	var out strings.Builder
	if err := runKeysOp(discardLog(t), "list", &keyFlags{}, nil, strings.NewReader(""), &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out.String(), "secret-gateway-value-1234") || strings.Contains(out.String(), "sk-full-secret-9999") {
		t.Errorf("平文キーが漏れています: %s", out.String())
	}
	if !strings.Contains(out.String(), "gatewayキー: 1 件") || !strings.Contains(out.String(), "openai") {
		t.Errorf("list出力が不足: %s", out.String())
	}
}

// TestFinishKeysOpRequiresRecipients はidentity・-recipient双方なしの変更保存を拒否する(SECRETS_DECRYPT_CMD環境の代替検証)。
func TestFinishKeysOpRequiresRecipients(t *testing.T) {
	sec := &secrets.Secrets{Version: 1, GatewayKey: "gk-a"}
	kf := &keyFlags{addr: resolveGatewayURL("")}
	var out strings.Builder
	err := finishKeysOp(discardLog(t), sec, filepath.Join(t.TempDir(), "x.age"), kf, &out)
	if err == nil || !strings.Contains(err.Error(), "recipient") {
		t.Errorf("recipient必須の拒否なし: %v", err)
	}
}

// TestReloadFailureIsNonFatal は-reloadで接続できない場合も保存済みとして成功扱いにする。
func TestReloadFailureIsNonFatal(t *testing.T) {
	keysTestEnv(t, nil)
	kf := &keyFlags{reload: true, addr: "http://127.0.0.1:1"}
	out, err := runCmd(t, "add", kf, []string{"gk-another-token"}, "")
	if err != nil {
		t.Fatalf("add(-reload): %v", err)
	}
	if !strings.Contains(out, "再読み込みに失敗") || !strings.Contains(out, "保存しました") {
		t.Errorf("出力: %s", out)
	}
	if got := loadFromDisk(t).AllGatewayKeys(); len(got) != 2 {
		t.Errorf("keys = %q", got)
	}
}

func TestNewGatewayKeyFormat(t *testing.T) {
	k1 := newGatewayKey()
	k2 := newGatewayKey()
	if !strings.HasPrefix(k1, "gk-") || !strings.HasPrefix(k2, "gk-") {
		t.Errorf("prefix不正: %s %s", k1, k2)
	}
	if k1 == k2 {
		t.Error("同一キーが生成された")
	}
}

// TestExtractKeyFlagsAllowsFlagsAfterPositional は `keys add VALUE -reload` のような後置フラグを解析できる。
func TestExtractKeyFlagsAllowsFlagsAfterPositional(t *testing.T) {
	kf, op, args, help, err := extractKeyFlags([]string{"add", "gk-value-1", "-reload", "-addr", "http://127.0.0.1:9999"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if help || op != "add" || len(args) != 1 || args[0] != "gk-value-1" {
		t.Errorf("op=%q args=%q", op, args)
	}
	if !kf.reload || kf.addr != "http://127.0.0.1:9999" {
		t.Errorf("kf = %+v", kf)
	}
}

// TestExtractKeyFlagsInlineAndRepeat は -addr=X 形式と -recipient の複数回指定を確認する。
func TestExtractKeyFlagsInlineAndRepeat(t *testing.T) {
	kf, op, args, help, err := extractKeyFlags([]string{"-recipient", "age1aaa", "list", "--recipient=age1bbb", "-addr=http://127.0.0.1:2"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if op != "list" || help || len(args) != 0 {
		t.Errorf("op=%q args=%q", op, args)
	}
	if kf.addr != "http://127.0.0.1:2" {
		t.Errorf("addr = %q", kf.addr)
	}
	if len(kf.recipients) != 2 || kf.recipients[0] != "age1aaa" || kf.recipients[1] != "age1bbb" {
		t.Errorf("recipients = %q", kf.recipients)
	}
}

// TestExtractKeyFlagsUnknownFlag は未知フラグを拒否する。
func TestExtractKeyFlagsUnknownFlag(t *testing.T) {
	if _, _, _, _, err := extractKeyFlags([]string{"list", "-nope"}); err == nil {
		t.Error("未知フラグが受理された")
	}
	if _, _, _, _, err := extractKeyFlags([]string{"set", "openai", "-unknown"}); err == nil {
		t.Error("位置引数後の未知フラグが受理された")
	}
}

// TestExtractKeyFlagsHelpKeepsDefaultAddr は -h 解析時に既定addrを採用することを確認する。
func TestExtractKeyFlagsHelpKeepsDefaultAddr(t *testing.T) {
	kf, _, _, help, err := extractKeyFlags([]string{"-h"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if kf.addr != resolveGatewayURL("") {
		t.Errorf("addr = %q, want %q", kf.addr, resolveGatewayURL(""))
	}
	if !help {
		t.Error("-h がhelpとして認識されていない")
	}
}
