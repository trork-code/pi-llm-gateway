# Changelog

pi-llm-gateway の変更履歴。日付は JST、括弧内はコミットハッシュ。
形式は [Keep a Changelog](https://keepachangelog.com/ja/1.1.0/) に準拠。

## [Unreleased]

### 追加 — gateway keys 管理CLI(鍵の追加・削除・棚卸し)

- `gateway keys list`: 鍵の棚卸し。gatewayキー・provider上流キーの設定状況を、
  値そのものではなく指紋(先頭4文字 + SHA-256短縮)で表示
- `gateway keys add [VALUE]`: gatewayキーを追加。省略時はランダム生成
  (`gk-` + 128bit Base64URL)して一度だけ表示。重複追加は no-op
- `gateway keys remove VALUE`: gatewayキーを削除。完全一致 or ユニーク前方一致。
  最後の1本の削除はロックアウト防止のため拒否
- `gateway keys set PROVIDER [VALUE|-]`: `api_keys.<provider>` を設定。
  `-`/省略時は標準入力から1行(シェル履歴に鍵が残らない)
- `gateway keys unset PROVIDER`: `api_keys.<provider>` を削除
- 共通フラグ: `-reload`(保存後に `POST /admin/reload` を稼働中gatewayへ通知)、
  `-addr URL`(既定 `http://127.0.0.1:18080`、env `GATEWAY_RELOAD_ADDR`)、
  `-recipient age1...`(`SECRETS_DECRYPT_CMD` 使用時の再暗号化先、複数回指定可)
- フラグは位置引数の**前後どちら**でも可(標準flagパッケージの制限を回避する専用パーサ `extractKeyFlags`)
- 保存は `internal/secrets.Save`(新規): YAML再生成 → age再暗号化(armor) →
  0600一時ファイル → rename の原子的書き戻し。レガシー(version未指定)ファイルは version 1 に昇格
- 復号済みSecretsは処理後 `Zero()`(サーバーと同じ経路)、`memguard.Purge()` も併用
- 制限: `SECRETS_DECRYPT_CMD` は再暗号化用の公開鍵が得られないため、
  identity系env(AGE_IDENTITY_FILE 等)経由でない場合は `-recipient` が必須
- テスト: keys_cmd_test.go(追加/削除/ロックアウト/前方一致曖昧/stdin・set・unset/list の値非表示/
  -reload 失敗時の非致命/フラグ解析)、save_test.go(Save→Load ラウンドトリップ/原子的差し替え/version昇格)

### 変更

- `internal/secrets` の YAML タグに `omitempty` を導入(編集後の再保存時に空フィールドを出さない)

### 実機検証

- `/tmp/gwtest`(ollamacloud 実キー環境)で以下を確認:
  list表示、ランダム生成キーの追加→そのキーで `/v1/models` 200、
  `set Provider`(stdin)、`add -reload` で稼働中gatewayへの即反映
  (通知後すぐ新キーで200)、`remove <prefix> -reload`、`unset Provider`

## [0.1.0] — 2026-09-02 〜 2026-09-16

### 2026-09-16

- **テスト**: `internal/sse` / `internal/apierr` の単体テスト追加(両パッケージ カバレッジ100%)。
  マルチバイトChunk・nil flusher・エラー伝播・`[DONE]`終端・code any型透過・JSON特殊文字エスケープなどを検証 (ec7e694)
- **テスト**: `scripts/local-e2e.sh` を追加。ダミーキー経路8項目(起動401 / models 200 /
  未知モデル400 / admin reload / 上流401透過(非stream+stream) / 総当たり429)をローカル実行で自動検証。
  `OLLAMA_API_KEY` を渡すと実リクエスト検証(日本語往復 + SSEチャンクassert)も追加 (2f08e78)
- **運用開始**: 実キー(ollamacloud / gpt-oss:120b)でライブ運用開始。
  日本語往復・SSEストリーミングを確認。学習事項: モデル名はタグ必須(`gpt-oss:120b` 等)、
  gpt-oss は reasoning が `max_tokens` を消費する (手動検証 / README クイックスタート)

### 2026-09-09

- **鍵保護の本格強化**: ホットリロード(`SIGHUP` + `POST /admin/reload`、fail-safe・旧Secrets.Zero())、
  `gateway -check` モード(復可否/config/欠落キー/鍵齢/identity公開鍵を報告)、
  `SECRETS_MAX_AGE_DAYS`(既定90)による鍵齢警告、`STRICT_KEYS=1` で欠落キー時起動拒否、
  欠落キー診断の一括化。CI に shellcheck、README環境変数表の整合テスト (293c299)
- **プロバイダー追加**: openrouter / groq / nvidia(いずれもOpenAI互換APIで `NewOpenAICompat` 流用)。
  configエイリアス: `pi-openrouter` / `pi-groq` / `pi-nvidia` (2836dd3)
- **鍵保護5改修**(コスト順):
  - egress許可リスト(設定済み `base_url` ドメイン以外への上流接続拒否、リダイレクト経由もガード) `EGRESS_ALLOWLIST`
  - 認証失敗レート制限(IP別失敗カウンタ、`AUTH_MAX_FAILURES` 既定20、TTL減衰、超過で429+Retry-After)
  - 監査ログの改ざん耐性(internal/audit ハッシュチェーン、全エントリに `audit_id`)
  - memguard 導入(実APIキーを mlock・メモリ内暗号化・ガードページ付き LockedBuffer へ格納、終了時 Purge)
  - `SECRETS_DECRYPT_CMD`(復号を age CLI へ委譲、YubiKey 等 ageプラグイン鍵に対応; 6559379 でREADME整備)
  (eaf1854/6559379)
- **鍵保護の焦点強化**: `AGE_IDENTITY_CMD`(コマンド stdout を identity として取得。Vault/AWS/GCP非依存、
  出力はメモリ上のみ・ログ禁止)、identity ソースの複数指定は起動拒否、
  `rotate-secrets.sh` に復号ラウンドトリップ検証(fail-closed)・複数recipient・`.bak` 退避・identityローテーション手順 (c5e3cdb)

### 2026-09-02

- **プロジェクト立ち上げ**: GitHub リポジトリ(trork-code/pi-llm-gateway、Private)作成、
  Go スキャフォールド(README/仕様書/config例/secrets手順)を push (9a46595)
- **フル実装**: 全パッケージ(apierr/auth/config/secrets/providers/sse/handlers + cmd/gateway)を
  テスト付きで実装。OpenAI素通し + streaming + auth が動作。Anthropic は 501 スタブ (43625be)
- **ollamacloud プロバイダー追加**: Ollama Cloud はOpenAI互換APIのため `NewOpenAICompat` で流用。
  config例: `pi-ollama → gpt-oss:120b` (2180d19)
- **APIキー保護の多層防御**(仕様書§7.5 対応):
  - identity のパスフレーズ保護(`AGE_PASSPHRASE`, scrypt)対応
  - `AGE_IDENTITY` env(シークレットマネージャ注入)対応
  - 秘密鍵ファイルは owner-only 必須・`secrets.yaml.age` は world-writable 拒否(hardening)
  - 起動時の RLIMIT_CORE=0 + 監査ログ(モデル/成否/所要時間のみ・内容非表示) + 平文バッファwipe/Secrets.Zero()
  - `BIND` 既定を 127.0.0.1 に(既定はループバックのみ)
  - TLS / mTLS(`MTLS_CA=RequireAndVerifyClientCert`)
  - CI に gitleaks、`scripts/rotate-secrets.sh`
  - auth は複数 gateway_keys 対応(constant-time)
  (0bf507b)

### 既知の制限(0.1.0時点)

- Anthropic プロバイダーは 501 スタブ(実装はロードマップ)
- 鍵値編集を伴う自動ローテーションは、シークレットマネージャ(`AGE_IDENTITY_CMD`)側で管理する構成を推奨(`rotate-secrets.sh` は手動実行)
- レート制限は IP 単位で、陽性リクエストも同一 IP からはブロックされる(手動テストは正常系→総当たりの順で)