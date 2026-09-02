// Command gateway はPi向けのOpenAI互換APIゲートウェイを起動する。
//
// 起動順序は仕様書(pi-llm-gateway-spec-go.md)どおり:
//  1. 環境変数を読む(PORT, AGE_IDENTITY_FILE など)
//  2. secretsを復号する(age) — 失敗したら即座に起動を止める
//  3. config/models.yamlを読んで検証する
//  4. providerごとのadapterを構築し、registryへ登録する
//  5. ルーター(chi)を組み立て、handlerとauthミドルウェアを紐付ける
//  6. HTTPサーバーを起動してリッスンを開始する
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	// 1. 環境変数
	port := envOr("PORT", "8080")
	configFile := envOr("CONFIG_FILE", "config/models.yaml")
	secretsFile := envOr("SECRETS_FILE", "secrets/secrets.yaml.age")
	identityFile := os.Getenv("AGE_IDENTITY_FILE")
	if identityFile == "" {
		return errors.New("環境変数 AGE_IDENTITY_FILE が未設定です(age秘密鍵のパスを指定してください)")
	}

	// 2. secrets復号 — 失敗したら即座に起動を止める(キーが無いまま動き出さないため)
	log.Info("secretsを復号しています", "file", secretsFile)
	sec, err := secrets.Load(secretsFile, identityFile)
	if err != nil {
		return fmt.Errorf("secretsの復号に失敗しました: %w", err)
	}
	if sec.GatewayKey == "" {
		return errors.New("secretsに gateway_key がありません")
	}

	// 3. config読み込み+検証
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("configの読み込みに失敗しました: %w", err)
	}

	// 4. providerごとのadapterを構築し、復号済み実キーを渡してregistryへ登録
	reg := providers.NewRegistry()
	for name, pc := range cfg.Providers {
		key := sec.APIKeys[name]
		if key == "" {
			log.Warn("実APIキーがsecretsにありません(このproviderへのリクエストは失敗します)", "provider", name)
		}
		switch name {
		case providers.ProviderOpenAI:
			reg.Register(providers.NewOpenAI(pc.BaseURL, key))
		case providers.ProviderAnthropic:
			reg.Register(providers.NewAnthropic(pc.BaseURL, key))
		default:
			return fmt.Errorf("未知のprovider %q がconfigにあります", name)
		}
	}

	// 5. ルーター組み立て: authをミドルウェアとして先に登録し、handlerを紐付ける
	h := handlers.New(cfg, reg, log)
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(auth.Middleware(sec.GatewayKey))
	r.Post("/v1/chat/completions", h.ChatCompletions)
	r.Get("/v1/models", h.Models)

	// 6. HTTPサーバー起動(Graceful shutdown付き)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
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

	log.Info("gatewayを起動しました", "addr", ":"+port, "default_model", cfg.DefaultModel, "models", len(cfg.Models))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
