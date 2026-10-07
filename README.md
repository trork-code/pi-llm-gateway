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
| `POST /admin/reload` | 鍵とconfigを再読み込み（gatewayキー認証）。SIGHUPでも可。失敗時は旧状態維持 |
| `POST /admin/shutdown` | 稼働中リクエスト完了後に安全停止（gatewayキー認証）。`gateway down` が使用 |
| `GET /healthz` | 認証不要の稼働確認。uptime・モデル構成・鍵数を返す（鍵値は含まない） |

- **認証** — `Authorization: Bearer <gatewayキー>` をチェック。不一致は401
- **モデル解決** — `model`（例: `pi-default`）を `config/models.yaml` で実プロバイダー/モデルに変換
- **実キーの管理** — 実APIキーは `secrets/secrets.yaml.age`（age暗号化）で管理し、起動時のみメモリ上で復号。ディスクに平文を書き出さない
- **エラー形式** — すべて `{ "error": { "message", "type", "code" } }` のOpenAI互換形

## 対応プロバイダー

| provider | 実装 | 備考 |
|---|---|---|
| `openai` | ✅ | 素通し（model差し替えのみ） |
| `ollamacloud` | ✅ | Ollama Cloud (`https://ollama.com/v1`)。OpenAI互換APIとして素通し。モデル名はollama.comのタグ名必須（例: `gpt-oss:120b`。タグ無し `gpt-oss` は上流404） |
| `openrouter` | ✅ | OpenRouter (`https://openrouter.ai/api/v1`)。OpenAI互換APIとして素通し。modelは `vendor/model` 形式 |
| `groq` | ✅ | Groq (`https://api.groq.com/openai/v1`)。OpenAI互換APIとして素通し |
| `nvidia` | ✅ | NVIDIA NIM (`https://integrate.api.nvidia.com/v1`)。OpenAI互換APIとして素通し。modelは `vendor/model` 形式 |
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

## クイックスタート（実機検証済み）

```bash
# 1. 鍵ペアを作る(age CLIが必要。秘密鍵はリポジトリ外へ)
age-keygen -o identity.txt
# 2. 実キーを暗号化(公開鍵は age-keygen -y identity.txt で取得)
cat > secrets.yaml <<'EOF'
gateway_key: <任意の合言葉>
api_keys:
  ollamacloud: <ollama.comのAPIキー>
EOF
age -r "$(age-keygen -y identity.txt)" -o secrets.yaml.age secrets.yaml && rm secrets.yaml
# 3. 起動
AGE_IDENTITY_FILE=identity.txt BIND=127.0.0.1 PORT=18080 ./gateway
# 4. 呼ぶ(クライアントはgatewayキーとモデル別名だけ知っていればよい)
curl http://127.0.0.1:18080/v1/chat/completions \
  -H "Authorization: Bearer <gatewayキー>" \
  -H "Content-Type: application/json" \
  -d '{"model":"pi-ollama","messages":[{"role":"user","content":"こんにちは"}]}'
```

OpenAI SDKやPi側設定では `base_url: http://127.0.0.1:18080/v1` + `api_key: <gatewayキー>` を指定するだけ。

## ローカル実機E2Eテスト

起動→認証→モデル解決→上流透過→ストリーミング→レート制限→停止までを自動検証する。
実キーがなくても動作し、`OLLAMA_API_KEY` を渡すと実リクエスト(非stream/stream)まで確認できる。

```bash
bash scripts/local-e2e.sh                          # ダミーキーで負経路+上流エラー透過を証明
OLLAMA_API_KEY=<実キー> bash scripts/local-e2e.sh   # 実リクエストも証明(ollamacloud/gpt-oss:120b)
```

2026-09にこの手順で実機検証済み: 認証(401/200)、未知モデル400、上流401透過、admin/reload、日本語往復(content「テスト成功」)、SSEチャンク配信、総当たり20回から429。

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
| `PORT` | `8080` | リッスンポート。`gateway status` / `up` / `down` / keys `-reload` の通知先もここから自動決定 |
| `GATEWAY_ADDR` | （任意） | CLIが自己接続するgatewayのURL（例: `https://gw.example:18080`）。未指定なら `http://127.0.0.1:<PORT>` |
| `BIND` | `127.0.0.1` | リッスンアドレス。LAN公開時のみ`0.0.0.0`等に変更 |
| `CONFIG_FILE` | `config/models.yaml` | 非秘匿設定 |
| `SECRETS_FILE` | `secrets/secrets.yaml.age` | 暗号化secrets |
| `AGE_IDENTITY_FILE` | （必須） | age秘密鍵ファイル |
| `AGE_IDENTITY` | （任意） | 秘密鍵の内容そのもの(シークレットマネージャ経由の注入用) |
| `AGE_IDENTITY_CMD` | （任意） | コマンドの標準出力を秘密鍵として利用(Vault/AWS/GCP等)。鍵をディスクに常駐させない |
| `AGE_PASSPHRASE` | （任意） | `age -p`で保護したidentityの復号パスフレーズ |
| `STRICT_KEYS` | （任意） | `1`を指定すると、1つでもapi_keysが欠けているproviderがあったら起動を中止 |
| `SECRETS_MAX_AGE_DAYS` | `90` | secretsファイルの経過日数がこの値を超えたらローテーションを警告（`0`で無効） |
| `SECRETS_DECRYPT_CMD` | （任意） | secretsの復号を外部コマンド(age CLI等)に委譲。YubiKey等のageプラグイン鍵に対応 |
| `EGRESS_ALLOWLIST` | `1` | 上流接続を設定済みbase_urlのドメインに限定（config改ざん時の実キー持ち出し防止）。`0`で無効 |
| `AUTH_MAX_FAILURES` | `20` | IPごとの認証失敗しきい値。超過で429（ウィンドウ1分）。`0`で無効 |
| `TLS_CERT` / `TLS_KEY` | （任意） | 指定するとHTTPSで起動 |
| `MTLS_CA` | （任意） | クライアント証明書検証用CA。指定するとmTLS必須になる |

### 鍵の再読み込み（ホットリロード）

gatewayを**再起動せずに**鍵とconfigを差し替えられる:

```bash
# (a) SIGHUPを送る（Linux/macOS）
kill -HUP $(pgrep pi-llm-gateway)

# (b) gatewayキー認証付きの管理エンドポイント
curl -X POST http://127.0.0.1:8080/admin/reload -H "Authorization: Bearer <gatewayキー>"
```

- 再読み込み中に復号・検証が失敗した場合は**旧状態を維持する**（fail-safe）
- 差し替え後、旧鍵はメモリからゼロ化（ベストエフォート）
- `AGE_IDENTITY_CMD` 構成なら、再読み込みのたびにシークレットマネージャから最新の鍵を取得する
- 起動前・再起動後の確認は `gateway -check` で実施できる（復号可否・config・キー欠落・鍵齢を報告）

### 鍵の管理CLI（`gateway keys`）

secrets.yaml.ageを手編集せずに鍵を追加・削除できる管理サブコマンド。
環境変数はサーバーと共通（`SECRETS_FILE` / `AGE_IDENTITY_FILE` 等）。変更はage再暗号化され**原子的に書き戻される**。

```bash
# 鍵の棚卸し（値は指紋のみ表示）
gateway keys list

# gatewayキーを追加（省略時はランダム生成して一度だけ表示）
gateway keys add              # → gk-xxxxxxxx（安全な場所に保管）
gateway keys add gk-mytoken   # 手動指定も可

# 稼働中なら変更を即反映（POST /admin/reloadを通知）
gateway keys add gk-new-key -reload

gateway keys remove 961f      # 完全一致 or ユニーク前方一致で削除
                              # 最後の1本はロックアウト防止で拒否
gateway keys set ollamacloud <キー>          # provider上流キーを設定
echo <キー> | gateway keys set openrouter -  # 標準入力経由も可（履歴に残らない）
gateway keys unset openrouter               # provider上流キーを削除
```

フラグ:

| フラグ | 既定 | 説明 |
|---|---|---|
| `-reload` | （なし） | 保存後に稼働中gatewayへ `POST /admin/reload` を通知（失敗時は警告のみ） |
| `-addr URL` | `http://127.0.0.1:<PORT|8080>` | 再読み込み先。env `GATEWAY_ADDR`（旧 `GATEWAY_RELOAD_ADDR`）でも指定可 |
| `-quiet` | （なし） | INFOログ非表示（CI・agent連携向け） |
| `-recipient age1...` | （identity由来） | `SECRETS_DECRYPT_CMD` 使用時の再暗号化先。複数回指定可 |

注意:
- `SECRETS_DECRYPT_CMD` は復号済み平文しか得られないため、identity系env経由でない場合は `-recipient` が必須
- フラグはサブコマンドや位置引数の**前後どちら**でも書ける（`keys add gk-x -reload` も可）
- 鍵の値は履歴に残らない標準入力経由を推奨（`set PROVIDER -`）
- `-reload` 未指定時は「変更は保存済み。反映には -reload を付けて再実行」とヒントが出る

### 起動・停止・状態（`gateway up` / `status` / `down`）

pi coding agentからの利用を想定した最低手順コマンド。接続先は **PORT envから自動決定**
（`http://127.0.0.1:<PORT|8080>`）。プロバイダーの切り替えはpiのモデル選択だけで行える
（`/v1/models` がエイリアス一覧を列挙するため、pi側でモデルを変えるとgatewayが対応providerへ振り分ける）。

```bash
gateway up        # 稼働していなければバックグラウンド起動→READY表示（冪等。セッション先頭で1回でOK）
gateway status    # 稼働中: URL・uptime・エイリアス→provider割当・鍵数を表示。未起動でも正常終了
gateway down      # 稼働中gatewayを安全に停止（稼働中リクエスト完了後）※gatewayキー認証
```

- `up` の稼働確認・`status` は認証不要の `GET /healthz` を使う（監視ツールからも叩ける）
- `down` のキー解決順: `-key` → env `GATEWAY_KEY` → secrets復号（identity envがあれば1本目）
- `up` 起動分のサーバーログは `gateway.log`（`-log PATH` で変更）
- TLS構成（自己署名含む）では `GATEWAY_ADDR=https://…` を設定しておくと、各CLIがそのURLへ接続
- Windows起動時に自動で立ち上げたい場合は、`gateway up` をスタートアップに登録（冪等なので毎回走らせて安全）

### 定期ローテーション（systemd timer例）

```ini
# /etc/systemd/system/pi-gateway-rotate.timer
[Timer]
OnCalendar=monthly
Persistent=true
[Install]
WantedBy=timers.target
```

※ 鍵値の編集を伴う自動ローテーションは、シークレットマネージャ（`AGE_IDENTITY_CMD`）側で
管理する構成が向く。編集不要のidentityローテーションのみtimer化するのが安全。

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