# pi-llm-gateway

Pi向けの **OpenAI互換APIゲートウェイ（Go実装）**。

Piから見るとただの「OpenAI互換API」。実際には裏側で本物のAPIキーを隠し持ち、
`config/models.yaml` の設定を書き換えるだけで、Pi側の設定は一切変えずに
実際に使うプロバイダー/モデルを切り替えられる中継サーバー。

## 機能

| エンドポイント | やること |
|---|---|
| `POST /v1/chat/completions` | リクエストを上流プロバイダーへ中継。streaming / 非streaming 両対応 |
| `GET /v1/models` | `config/models.yaml` のエイリアス一覧をOpenAI互換形式で返す |

- **認証** — `Authorization: Bearer <gatewayキー>` をチェック。不一致は401
- **モデル解決** — `model`（例: `pi-default`）を `config/models.yaml` で実プロバイダー/モデルに変換
- **実キーの管理** — 実APIキーは `secrets/secrets.yaml.age`（age暗号化）で管理し、起動時のみメモリ上で復号。ディスクに平文を書き出さない
- **エラー形式** — すべて `{ "error": { "message", "type", "code" } }` のOpenAI互換形

## 対応プロバイダー

| provider | 実装 | 備考 |
|---|---|---|
| `openai` | ✅ | 素通し（model差し替えのみ） |
| `ollamacloud` | ✅ | Ollama Cloud (`https://ollama.com/v1`)。OpenAI互換APIとして素通し |
| `anthropic` | 🚧 | スタブ(501)。ロードマップstep4で実装 |

OpenAI互換のAPIを持つ他社サービスは、`internal/providers/openai.go` の `NewOpenAICompat` でprovider名を差し替えるだけで追加できる。

## フォルダ構成

```
pi-llm-gateway/
├── cmd/gateway/main.go        # 起動処理はここだけ
├── config/models.yaml         # どのエイリアスがどのプロバイダー/モデルか(非秘匿)
├── secrets/secrets.yaml.age   # 実キー一式(age暗号化済み。中身は秘匿)
```
├── internal/
│   ├── config/    # models.yamlを読む
│   ├── secrets/   # secrets.yaml.ageを復号する
│   ├── auth/      # gatewayキーをチェックする
│   ├── handlers/  # /v1/chat/completions などのエンドポイント処理
│   ├── providers/ # OpenAI, Anthropicなど各社ごとの変換ロジック
│   └── sse/       # ストリーミング配信の共通処理
```

## 使うライブラリ

- HTTPルーティング: `chi`（軽量で書きやすい）
- YAML読み込み: `gopkg.in/yaml.v3`
- 鍵の暗号化: `filippo.io/age`（ageの公式Go実装）
- ログ: `log/slog`（標準ライブラリ）

## セットアップ（シークレット管理）

1. **鍵を作る** — `age-keygen` で秘密鍵(identity)と公開鍵(recipient)のペアを作る。秘密鍵はリポジトリ外で管理
2. **暗号化する** — 実キーを書いたyamlを公開鍵で暗号化して `secrets/secrets.yaml.age` にする（このファイルはコミットしてよい）。平文は削除
3. **起動時に復号する** — gateway起動時に環境変数 `AGE_IDENTITY_FILE` で指定した秘密鍵で復号

## APIキー保護（多層防御）の実装状況

アーキテクチャ仕様書§7.5の多層防御のうち、コード・CIで対応済みのもの:

| 層 | 状態 | 実装 |
|---|---|---|
| ① 保管時の暗号化 | ✅ | `secrets.yaml.age`(age)。identity自体を`age -p`で保護する場合、`AGE_PASSPHRASE`で復号対応 |
| ② 鍵の受け渡し | 🔶 | 秘密鍵はファイル(`AGE_IDENTITY_FILE`)/環境変数(`AGE_IDENTITY`)/コマンド実行(`AGE_IDENTITY_CMD`、Vault/AWS/GCP等から起動の瞬間だけ取得)の3系統。重複指定は起動拒否。ローテーション先recipientを複数指定し「決まった環境の鍵でしか復号できない」構成も可 |
| ③ ファイル権限 | ✅ | 秘密鍵は所有者のみ必須(違反は起動失敗)、`secrets.yaml.age`は他者書き込み可能なら起動拒否 |
| ④ プロセス・メモリ | ✅ | 起動時にコアダンプ無効化(RLIMIT_CORE=0)、キーはログに絶対に出さない、監査ログはモデル・成否・所要時間のみ、平文バッファのゼロ化(ベストエフォート) |
| ⑤ ネットワーク | 🔶 | `BIND`既定`127.0.0.1`、TLS(`TLS_CERT`/`TLS_KEY`)、mTLS(`MTLS_CA`＝クライアント証明書必須)対応 |
| ⑥ キー権限 | 📋 | 運用: プロバイダー側で最小権限キーを発行 |
| ⑦ 監視 | 📋 | 運用: 監査ログ(リクエストごとのモデル・成否・所要時間)を収集して監視 |
| ⑧ 供給網 | ✅ | CIにgitleaks(secret scanning)。依存は最小限(3パッケージ)に固定 |
| ⑨ ローテーション | ✅ | `scripts/rotate-secrets.sh`(復号→編集→再暗号化→**復号ラウンドトリップ検証**→バックアップ退避)。identityローテーション対応 |

### デプロイ時のハードニング例（systemd）

```ini
# /etc/systemd/system/pi-llm-gateway.service
[Service]
User=pi-gateway
LimitCORE=0
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
Environment=CONFIG_FILE=/etc/pi-llm-gateway/models.yaml
Environment=SECRETS_FILE=/etc/pi-llm-gateway/secrets.yaml.age
Environment=AGE_IDENTITY_FILE=/etc/pi-llm-gateway/identity.age
EnvironmentFile=-/etc/pi-llm-gateway/secret.env   # AGE_PASSPHRASE等(600, pi-gateway所有)
ExecStart=/usr/local/bin/pi-llm-gateway
```

### 環境変数一覧

| 変数 | 既定 | 説明 |
|---|---|---|
| `PORT` | `8080` | リッスンポート |
| `BIND` | `127.0.0.1` | リッスンアドレス。LAN公開時のみ`0.0.0.0`等に変更 |
| `CONFIG_FILE` | `config/models.yaml` | 非秘匿設定 |
| `SECRETS_FILE` | `secrets/secrets.yaml.age` | 暗号化secrets |
| `AGE_IDENTITY_FILE` | （必須） | age秘密鍵ファイル |
| `AGE_IDENTITY` | （任意） | 秘密鍵の内容そのもの(シークレットマネージャ経由の注入用) |
| `AGE_IDENTITY_CMD` | （任意） | コマンドの標準出力を秘密鍵として利用(Vault/AWS/GCP等)。鍵をディスクに常駐させない |
| `AGE_PASSPHRASE` | （任意） | `age -p`で保護したidentityの復号パスフレーズ |
| `TLS_CERT` / `TLS_KEY` | （任意） | 指定するとHTTPSで起動 |
| `MTLS_CA` | （任意） | クライアント証明書検証用CA。指定するとmTLS必須になる |

## 開発

```bash
go build ./...          # ビルド
go test ./...           # テスト
go run ./cmd/gateway    # 起動（AGE_IDENTITY_FILE と secrets/secrets.yaml.age が必須）
```

CI（.github/workflows/ci.yml）が gofmt / go vet / go build / go test を検証する。

## 実装ロードマップ

- [x] OpenAI素通しの最小サーバー（model差し替えのみのパススルー）
- [x] gatewayキー認証（auth）
- [x] streaming対応（sse — OpenAI素通し）
- [x] Anthropicアダプターのスタブ（現在は501を返す）
- [ ] Anthropicアダプター実装（OpenAI⇄Anthropic変換。models.yaml書き換えだけで切り替わるところまで）
- [ ] 平文 `.env` キーを `secrets.yaml.age` に置き換え

詳細な設計は [pi-llm-gateway-spec-go.md](pi-llm-gateway-spec-go.md) を参照。