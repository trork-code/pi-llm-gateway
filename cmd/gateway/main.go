// Command gateway はPi向けのOpenAI互換APIゲートウェイを起動する。
//
// 起動順序(pi-llm-gateway-architecture.md §4 / APIキー保護§7.5):
//  1. OSレベルの鍵保護を有効化(コアダンプ無効化)
//  2. 環境変数を読む(PORT, BIND, AGE_IDENTITY_FILE/AGE_IDENTITY, AGE_PASSPHRASE, TLS_* など)
//  3. secretsを復号する(age) — 失敗したら即座に起動を止める
//  4. config/models.yamlを読んで検証する
//  5. providerごとのadapterを構築し、registryへ登録する
//  6. ルーター(chi)を組み立て、監査ログ/authミドルウェア/handlerを紐付ける
//  7. HTTPサーバーを起動してリッスンを開始する(TLS/mTLSオプション付き)
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/trork-code/pi-llm-gateway/internal/auth"
	"github.com/trork-code/pi-llm-gateway/internal/config"
	"github.com/trork-code/pi-llm-gateway/internal/handlers"
	"github.com/trork-code/pi-llm-gateway/internal/hardening"
	"github.com/trork-code/pi-llm-gateway/internal/providers"
	"github.com/trork-code/pi-llm-gateway/internal/secrets"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gatewayの起動に失敗しました", "error", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.Default()

	// 1. OSレベルの鍵保護(多層防御④): コアダンプ無効化
	// クラッシュ時にメモリ上の鍵がダンプファイルに残るのを防ぐ
	if err := hardening.DisableCoreDumps(); err != nil {
		log.Warn("コアダンプの無効化に失敗しました(systemdのLimitCORE=0を推奨)", "error", err)
	}

	// 2. 環境変数
	port := envOr("PORT", "8080")
	bind := envOr("BIND", "127.0.0.1") // 多層防御⑤: 既定はループバックのみでリッスン
	configFile := envOr("CONFIG_FILE", "config/models.yaml")
	secretsFile := envOr("SECRETS_FILE", "secrets/secrets.yaml.age")
	identityFile := os.Getenv("AGE_IDENTITY_FILE")
	identityInline := os.Getenv("AGE_IDENTITY")  // 多層防御②: シークレットマネージャからの注入用
	identityCmd := os.Getenv("AGE_IDENTITY_CMD") // 多層防御②: コマンド実行で取得(Vault/AWS/GCP等)
	passphrase := os.Getenv("AGE_PASSPHRASE")    // 多層防御①: age -p で保護されたidentityの復号用

	// 秘密鍵ソースは1つだけ指定する(意図しない鍵ソースの混在を防ぐため曖昧な指定は拒否)
	var sources []string
	if identityFile != "" {
		sources = append(sources, "AGE_IDENTITY_FILE")
	}
	if identityInline != "" {
		sources = append(sources, "AGE_IDENTITY")
	}
	if identityCmd != "" {
		sources = append(sources, "AGE_IDENTITY_CMD")
	}
	if len(sources) == 0 {
		return errors.New("環境変数 AGE_IDENTITY_FILE / AGE_IDENTITY / AGE_IDENTITY_CMD のいずれかを設定してください(age秘密鍵を指定してください)")
	}
	if len(sources) > 1 {
		return fmt.Errorf("秘密鍵の指定が重複しています: %s(1つだけ指定してください)", strings.Join(sources, ", "))
	}

	// 秘密鍵ソース: ファイル(権限チェック付き)/コマンド実行/インライン(多層防御②③)
	var identitySrc io.Reader
	switch {
	case identityFile != "":
		if err := hardening.CheckOwnerOnly(identityFile); err != nil {
			return err
		}
		f, err := os.Open(identityFile)
		if err != nil {
			return fmt.Errorf("age秘密鍵を開けません(%s): %w", identityFile, err)
		}
		defer f.Close()
		identitySrc = f
		log.Info("秘密鍵をファイルから読み込みます", "file", identityFile, "passphrase_protected", passphrase != "")
	case identityCmd != "":
		cmdCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		src, err := secrets.IdentityCommand(cmdCtx, identityCmd)
		if err != nil {
			return err
		}
		identitySrc = src
		log.Info("秘密鍵をAGE_IDENTITY_CMDで取得します(シークレットマネージャ経由を想定)")
	default:
		identitySrc = strings.NewReader(identityInline)
		log.Info("秘密鍵を環境変数から読み込みます(シークレットマネージャ経由を想定)")
	}

	// 3. secrets復号 — 失敗したら即座に起動を止める(キーが無いまま動き出さないため)
	log.Info("secretsを復号しています", "file", secretsFile)
	sec, err := secrets.Load(secrets.LoadOptions{
		EncryptedPath: secretsFile,
		Identity:      identitySrc,
		Passphrase:    passphrase,
	})
	if err != nil {
		return fmt.Errorf("secretsの復号に失敗しました: %w", err)
	}
	gatewayKeys := sec.AllGatewayKeys()
	if len(gatewayKeys) == 0 {
		return errors.New("secretsに gateway_key / gateway_keys がありません")
	}

	// 4. config読み込み+検証
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("configの読み込みに失敗しました: %w", err)
	}

	// 5. providerごとのadapterを構築し、復号済み実キーを渡してregistryへ登録
	reg := providers.NewRegistry()
	for name, pc := range cfg.Providers {
		key := sec.APIKeys[name]
		if key == "" {
			log.Warn("実APIキーがsecretsにありません(このproviderへのリクエストは失敗します)", "provider", name)
		}
		switch name {
		case providers.ProviderOpenAI:
			reg.Register(providers.NewOpenAI(pc.BaseURL, key))
		case providers.ProviderOllamaCloud:
			reg.Register(providers.NewOllamaCloud(pc.BaseURL, key))
		case providers.ProviderAnthropic:
			reg.Register(providers.NewAnthropic(pc.BaseURL, key))
		default:
			return fmt.Errorf("未知のprovider %q がconfigにあります", name)
		}
	}

	// 6. ルーター組み立て: 監査ログ→auth→handler の順
	h := handlers.New(cfg, reg, log)
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(handlers.RequestLogger(log))
	r.Use(chimw.Recoverer)
	r.Use(auth.Middleware(gatewayKeys))
	r.Post("/v1/chat/completions", h.ChatCompletions)
	r.Get("/v1/models", h.Models)

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Info("シャットダウン要求を受信しました")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Info("gatewayを起動しました", "addr", srv.Addr, "tls", tlsCert != "", "default_model", cfg.DefaultModel, "models", len(cfg.Models))
	var serveErr error
	if tlsCert != "" {
		serveErr = srv.ListenAndServeTLS(tlsCert, tlsKeyFile)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	// 停止後に鍵の残存を最小化(ベストエフォート。多層防御④)
	sec.Zero()
	return nil
}

// buildTLSConfig はTLS(必要ならmTLS)の設定を組み立てる(多層防御⑤)。
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
