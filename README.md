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

## フォルダ構成

```
pi-llm-gateway/
├── cmd/gateway/main.go        # 起動処理はここだけ
├── config/models.yaml         # どのエイリアスがどのプロバイダー/モデルか(非秘匿)
├── secrets/secrets.yaml.age   # 実キー一式(age暗号化済み。中身は秘匿)
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

## 実装ロードマップ

- [ ] OpenAI素通しの最小サーバー
- [ ] gatewayキー認証（auth）
- [ ] streaming対応（sse）
- [ ] Anthropicアダプター追加、models.yaml書き換えだけで切り替え可能に
- [ ] 平文 `.env` キーを `secrets.yaml.age` に置き換え

詳細な設計は [pi-llm-gateway-spec-go.md](pi-llm-gateway-spec-go.md) を参照。