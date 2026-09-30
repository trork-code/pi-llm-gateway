// Command gateway はPi向けのOpenAI互換APIゲートウェイを起動する。
//
// 起動順序:
//  1. OSレベルの鍵保護を有効化(コアダンプ無効化・鍵メモリのmlock保護)
//  2. 環境変数を読む(PORT, BIND, AGE_IDENTITY_*, AGE_PASSPHRASE, TLS_* など)
//  3. secretsを復号する(age) — 失敗したら即座に起動を止める
//  4. config/models.yamlを読んで検証し、上流egress許可リストを設定する
//  5. providerごとのadapterを構築し、registryへ登録する
//  6. ルーター(chi)を組み立て、監査ログ/authミドルウェア/handlerを紐付ける
//  7. HTTPサーバーを起動してリッスンを開始する(TLS/mTLSオプション付き)
//
// 鍵の保守:
//   - `gateway -check` で起動前に鍵・configの検証ができる
//   - SIGHUP または POST /admin/reload(gatewayキー認証)で鍵とconfigを再起動なしで差し替え
//     (再読み込みに失敗した場合は旧状態を維持する fail-safe)
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/awnumar/memguard"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/trork-code/pi-llm-gateway/internal/apierr"
	"github.com/trork-code/pi-llm-gateway/internal/audit"
	"github.com/trork-code/pi-llm-gateway/internal/auth"
	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/handlers"
	"github.com/trork-code/pi-llm-gateway/internal/hardening"
	"github.com/trork-code/pi-llm-gateway/internal/providers"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

func main() {
	// 鍵管理サブコマンド(サーバーを起動しない): gateway keys list|add|remove|set|unset
	if len(os.Args) > 1 && os.Args[1] == "keys" {
		if err := keysCmd(os.Args[2:]); err != nil {
			slog.Error("keysサブコマンドが失敗しました", "error", err)
			os.Exit(1)
		}
		return
	}
	var check bool
	flag.BoolVar(&check, "check", false, "configと鍵を検証して終了する(サーバーは起動しない)")
	flag.Parse()
	if err := run(check); err != nil {
		slog.Error("gatewayの起動に失敗しました", "error", err)
		os.Exit(1)
	}
}

// appEnv は鍵とconfigの読み込みに必要な環境設定。
type appEnv struct {
	configFile  string
	secretsFile string
	passphrase  string
	decryptCmd  string // SECRETS_DECRYPT_CMD(age CLI等による復号。YubiKeyプラグイン連携)
	strictKeys  bool
	maxAgeDays  int64
	ident       identitySource
}

// identitySource は秘密鍵の取得経路。
type identitySource struct {
	kind   string // "file" / "cmd" / "env"
	file   string
	inline string
	cmd    string
}

func run(check bool) error {
	log := slog.Default()

	// 1. OSレベルの鍵保護
	// - コアダンプ無効化(クラッシュ時にメモリ上の鍵が残るのを防ぐ)
	// - 正常終了時にmemguardの保護メモリ(実APIキー)をすべてパージ
	defer memguard.Purge()
	if err := hardening.DisableCoreDumps(); err != nil {
		log.Warn("コアダンプの無効化に失敗しました(systemdのLimitCORE=0を推奨)", "error", err)
	}

	// 2. 環境変数
	port := envOr("PORT", "8080")
	bind := envOr("BIND", "127.0.0.1") // 既定はループバックのみでリッスン
	app := &appEnv{
		configFile:  envOr("CONFIG_FILE", "config/models.yaml"),
		secretsFile: envOr("SECRETS_FILE", "secrets/secrets.yaml.age"),
		passphrase:  os.Getenv("AGE_PASSPHRASE"),
		decryptCmd:  os.Getenv("SECRETS_DECRYPT_CMD"),
		strictKeys:  os.Getenv("STRICT_KEYS") != "" || check,
		maxAgeDays:  envInt("SECRETS_MAX_AGE_DAYS", 90),
	}
	if app.decryptCmd != "" {
		// 復号をage CLI等に委譲する場合、identityはコマンド側で解決される
		log.Info("secretsを外部コマンドで復号します(SECRETS_DECRYPT_CMD。YubiKeyプラグイン等に対応)")
	} else {
		var err error
		if app.ident, err = resolveIdentitySource(log); err != nil {
			return err
		}
	}

	// 3〜5. Runtime構築(鍵・config・adapter) — 起動時と再読み込みで同じ経路を使う
	build := func() (*handlers.Runtime, error) {
		return buildRuntime(log, app)
	}
	rt, err := build()
	if err != nil {
		return err
	}

	// -check: 検証のみ行い、サーバーは起動しない
	if check {
		return reportCheck(log, rt, app)
	}

	// 6. ルーター組み立て: 監査ログ→auth→handler の順
	auditChain, err := audit.NewChain()
	if err != nil {
		return fmt.Errorf("監査チェーンの初期化に失敗しました: %w", err)
	}
	h := handlers.New(rt, log, auditChain)
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(handlers.RequestLogger(log, auditChain))
	r.Use(chimw.Recoverer)
	r.Use(auth.MiddlewareConfig(h.GatewayKeys, int(envInt("AUTH_MAX_FAILURES", 20)), time.Minute))
	r.Post("/v1/chat/completions", h.ChatCompletions)
	r.Get("/v1/models", h.Models)

	// 鍵とconfigの再読み込み(ホットリロード)。失敗時は旧状態を維持する
	reload := func() error {
		nrt, err := build()
		if err != nil {
			return err
		}
		old := h.Current()
		h.SwapRuntime(nrt)
		if old != nil && old.Secrets != nil {
			old.Secrets.Zero() // 差し替えられた旧鍵の残存を最小化(ベストエフォート)
		}
		log.Info("configと鍵を再読み込みしました",
			"models", len(nrt.Config.Models),
			"recipients", nrt.Secrets.IdentityRecipients)
		return nil
	}
	r.Post("/admin/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := reload(); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err.Error(), "api_error", "reload_failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "reloaded"})
	})

	// 7. HTTPサーバー起動(TLS/mTLSオプション付き, graceful shutdown付き)
	tlsCert := os.Getenv("TLS_CERT")
	tlsKeyFile := os.Getenv("TLS_KEY")
	if tlsCert != "" && tlsKeyFile == "" {
		return errors.New("TLS_CERT が設定されていますが TLS_KEY が未設定です")
	}
	srv := &http.Server{
		Addr:              net.JoinHostPort(bind, port),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if tlsCert != "" {
		tlsCfg, err := buildTLSConfig(os.Getenv("MTLS_CA"))
		if err != nil {
			return err
		}
		srv.TLSConfig = tlsCfg
		log.Info("TLSを有効化します", "mtls", os.Getenv("MTLS_CA") != "")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGHUP {
				log.Info("SIGHUPを受信しました: 鍵とconfigを再読み込みします")
				if err := reload(); err != nil {
					log.Error("再読み込みに失敗しました(旧状態を維持します)", "error", err)
				}
				continue
			}
			log.Info("シャットダウン要求を受信しました")
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
			return
		}
	}()

	log.Info("gatewayを起動しました",
		"addr", srv.Addr,
		"tls", tlsCert != "",
		"default_model", rt.Config.DefaultModel,
		"models", len(rt.Config.Models),
		"recipients", rt.Secrets.IdentityRecipients)
	var serveErr error
	if tlsCert != "" {
		serveErr = srv.ListenAndServeTLS(tlsCert, tlsKeyFile)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	// 停止後に鍵の残存を最小化(ベストエフォート。memguard.Purgeが保護メモリも掃除する)
	if rt := h.Current(); rt.Secrets != nil {
		rt.Secrets.Zero()
	}
	return nil
}

// buildRuntime はconfig・鍵・adapterを組み立て、不変スナップショットを返す。
// 起動時とホットリロードの両方で同じ経路を使う。
func buildRuntime(log *slog.Logger, app *appEnv) (*handlers.Runtime, error) {
	cfg, err := config.Load(app.configFile)
	if err != nil {
		return nil, fmt.Errorf("configの読み込みに失敗しました: %w", err)
	}

	// 上流egress許可リスト: 実キーが送られる先を設定済みproviderのドメインに限定する
	if err := applyEgressAllowlist(log, cfg); err != nil {
		return nil, err
	}

	opts := secrets.LoadOptions{
		EncryptedPath:  app.secretsFile,
		Passphrase:     app.passphrase,
		DecryptCommand: app.decryptCmd,
	}
	if app.decryptCmd == "" {
		idSrc, closeFn, err := app.ident.open()
		if err != nil {
			return nil, err
		}
		if closeFn != nil {
			defer closeFn()
		}
		opts.Identity = idSrc
	}

	sec, err := secrets.Load(opts)
	if err != nil {
		return nil, fmt.Errorf("secretsの復号に失敗しました: %w", err)
	}

	keys := sec.AllGatewayKeys()
	if len(keys) == 0 {
		return nil, errors.New("secretsに gateway_key / gateway_keys がありません")
	}
	log.Info("secretsを復号しました",
		"recipients", sec.IdentityRecipients,
		"gateway_keys", len(keys))

	reg := providers.NewRegistry()
	var missing []string
	for name, pc := range cfg.Providers {
		key := sec.APIKeys[name]
		if key == "" {
			missing = append(missing, name)
		}
		switch name {
		case providers.ProviderOpenAI:
			reg.Register(providers.NewOpenAI(pc.BaseURL, key))
		case providers.ProviderOllamaCloud:
			reg.Register(providers.NewOllamaCloud(pc.BaseURL, key))
		case providers.ProviderAnthropic:
			reg.Register(providers.NewAnthropic(pc.BaseURL, key))
		default:
			return nil, fmt.Errorf("未知のprovider %q がconfigにあります", name)
		}
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf("api_keys に実キーがありません: %s", strings.Join(missing, ", "))
		if app.strictKeys {
			return nil, errors.New(msg + "(STRICT_KEYS=1 が有効のため起動を中止します)")
		}
		log.Warn(msg + "(該当providerへのリクエストは失敗します)")
	}
	warnSecretsAge(log, app.secretsFile, app.maxAgeDays)

	return &handlers.Runtime{
		Config:      cfg,
		Registry:    reg,
		Secrets:     sec,
		GatewayKeys: keys,
	}, nil
}

// applyEgressAllowlist は上流接続を設定済みbase_urlのドメインに限定する。
// 実APIキーがAuthorizationヘッダで送られる先を、意図したプロバイダーだけに絞る。
func applyEgressAllowlist(log *slog.Logger, cfg *config.Config) error {
	if envOr("EGRESS_ALLOWLIST", "1") == "0" {
		log.Warn("上流egress許可リストを無効化しました(EGRESS_ALLOWLIST=0)")
		_ = providers.SetEgressAllowlist(nil)
		return nil
	}
	hosts := make([]string, 0, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		u, err := url.Parse(pc.BaseURL)
		if err != nil {
			return fmt.Errorf("provider %q のbase_url解析に失敗(%s): %w", name, pc.BaseURL, err)
		}
		if u.Hostname() == "" {
			return fmt.Errorf("provider %q のbase_urlにホストがありません(%s)", name, pc.BaseURL)
		}
		hosts = append(hosts, u.Hostname())
	}
	if err := providers.SetEgressAllowlist(hosts); err != nil {
		return err
	}
	log.Info("上流egress許可リストを有効化しました", "hosts", hosts)
	return nil
}

// resolveIdentitySource は秘密鍵の取得経路を環境変数から決める。
// ファイル/環境変数/コマンド実行の3系統で、重複指定は曖昧さ回避のため拒否する。
func resolveIdentitySource(log *slog.Logger) (identitySource, error) {
	file := os.Getenv("AGE_IDENTITY_FILE")
	inline := os.Getenv("AGE_IDENTITY")
	cmd := os.Getenv("AGE_IDENTITY_CMD")

	var found []string
	if file != "" {
		found = append(found, "AGE_IDENTITY_FILE")
	}
	if inline != "" {
		found = append(found, "AGE_IDENTITY")
	}
	if cmd != "" {
		found = append(found, "AGE_IDENTITY_CMD")
	}
	if len(found) == 0 {
		return identitySource{}, errors.New("環境変数 AGE_IDENTITY_FILE / AGE_IDENTITY / AGE_IDENTITY_CMD のいずれかを設定してください(age秘密鍵を指定してください)")
	}
	if len(found) > 1 {
		return identitySource{}, fmt.Errorf("秘密鍵の指定が重複しています: %s(1つだけ指定してください)", strings.Join(found, ", "))
	}

	switch found[0] {
	case "AGE_IDENTITY_FILE":
		log.Info("秘密鍵をファイルから読み込みます", "file", file, "passphrase_protected", os.Getenv("AGE_PASSPHRASE") != "")
		return identitySource{kind: "file", file: file}, nil
	case "AGE_IDENTITY_CMD":
		log.Info("秘密鍵をAGE_IDENTITY_CMDで取得します(シークレットマネージャ経由を想定)")
		return identitySource{kind: "cmd", cmd: cmd}, nil
	default:
		log.Info("秘密鍵を環境変数から読み込みます(シークレットマネージャ経由を想定)")
		return identitySource{kind: "env", inline: inline}, nil
	}
}

// open は秘密鍵を読み取り可能な状態にしてio.Readerを返す。
// 再読み込みのたびに呼ばれるため、シークレットマネージャ経由(cmd)は都度最新の鍵を取得する。
func (s identitySource) open() (io.Reader, func(), error) {
	switch s.kind {
	case "file":
		if err := hardening.CheckOwnerOnly(s.file); err != nil {
			return nil, nil, err
		}
		f, err := os.Open(s.file)
		if err != nil {
			return nil, nil, fmt.Errorf("age秘密鍵を開けません(%s): %w", s.file, err)
		}
		return f, func() { _ = f.Close() }, nil
	case "cmd":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r, err := secrets.IdentityCommand(ctx, s.cmd)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		return r, cancel, nil
	case "env":
		return strings.NewReader(s.inline), func() {}, nil
	default:
		return nil, nil, fmt.Errorf("未知のidentityソース: %s", s.kind)
	}
}

// reportCheck は検証結果だけを報告して終了する(-checkモード)。
func reportCheck(log *slog.Logger, rt *handlers.Runtime, app *appEnv) error {
	ageDays := int64(-1)
	if info, err := os.Stat(app.secretsFile); err == nil {
		ageDays = int64(time.Since(info.ModTime()).Hours() / 24)
	}
	log.Info("check結果: 検証OK",
		"models", len(rt.Config.Models),
		"providers", len(rt.Config.Providers),
		"recipients", rt.Secrets.IdentityRecipients,
		"secrets_file_age_days", ageDays,
		"strict_keys", app.strictKeys,
	)
	warnSecretsAge(log, app.secretsFile, app.maxAgeDays)
	return nil
}

// warnSecretsAge はsecretsファイルがローテーション期間を過ぎていたら警告する。
func warnSecretsAge(log *slog.Logger, path string, maxAgeDays int64) {
	if maxAgeDays <= 0 {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	days := int64(time.Since(info.ModTime()).Hours() / 24)
	if days > maxAgeDays {
		log.Warn("secretsファイルが古くなっています。ローテーションを検討してください",
			"age_days", days,
			"threshold_days", maxAgeDays,
			"hint", "bash scripts/rotate-secrets.sh <recipient>")
	}
}

// buildTLSConfig はTLS(必要ならmTLS)の設定を組み立てる。
// MTLS_CAが指定された場合、そのCAが署名したクライアント証明書を持つ接続のみ許可する。
func buildTLSConfig(clientCAFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if clientCAFile == "" {
		return cfg, nil
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("クライアントCA証明書を読めません(%s): %w", clientCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("クライアントCA証明書の形式が不正です(%s)", clientCAFile)
	}
	cfg.ClientCAs = pool
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	return cfg, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
