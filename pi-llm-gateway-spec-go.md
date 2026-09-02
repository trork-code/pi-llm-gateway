# Pi LLM Gateway — Go版 spec（わかりやすい版）

## これは何か
Piから見ると、ただの「OpenAI互換API」。実際には裏側で本物のAPIキーを隠し持ち、
`config`ファイルの設定だけで実際に使うプロバイダー/モデルを切り替えられる中継サーバー。

## リクエストが来たときの流れ

実装すべきことは、結局この6ステップに集約される。

1. Piから `POST /v1/chat/completions` が届く
2. ヘッダのgatewayキー（合言葉）をチェックする。違えば401を返して終了
3. リクエストの `model`（例: `"pi-default"`）を見て、「本当はどのプロバイダーのどのモデルを使うか」を`config/models.yaml`で調べる
4. そのプロバイダー用のアダプター（変換ロジック）を選ぶ
5. アダプターが、保管してある本物のAPIキーで実際のプロバイダーへリクエストを送る
6. `stream: true` なら結果を少しずつPiへ流す。そうでなければまとめて1回で返す

以降のセクションは、この6ステップをGoでどう組み立てるかの詳細。

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

## 部品ごとの役割

**config** — `config/models.yaml`を読み込むだけ。ここに秘密は一切書かない。

**secrets** — `secrets.yaml.age`を復号して、本物のAPIキーをメモリ上だけに持つ。ディスクには平文を書き出さない。

**auth** — リクエストヘッダの `Authorization: Bearer <キー>` を見て、gatewayキーと一致するか確認する。ここを通らないと先には進めない。

**providers** — プロバイダーごとに1ファイル(`openai.go`, `anthropic.go`)。やることは共通で「OpenAI形式のリクエストを受け取り、そのプロバイダー向けに送り、結果をOpenAI形式に戻して返す」。新しいプロバイダーを足すときは、ここに1ファイル追加するだけでいい。

**sse** — ストリーミング配信をどのプロバイダーでも同じ書き方で行うための共通処理。

## サーバー起動時の処理順序

`main.go`が起動時にやることは、この順番に依存関係がある。

1. 環境変数を読む(`PORT`, `AGE_IDENTITY_FILE`など)
2. secretsを復号する(age) — ここで失敗したら即座に起動を止める。キーが無いまま動き出すのを防ぐため
3. `config/models.yaml`を読んで検証する — 存在しないproviderを参照しているエイリアスがあればここで弾く
4. providerごとのadapter(openai, anthropic…)を作り、providers registryに登録する。このとき2で復号した実キーを各adapterに渡しておく
5. ルーター(chi)を組み立て、`/v1/chat/completions`と`/v1/models`にhandlerを紐付け、authをミドルウェアとして登録する
6. HTTPサーバーを起動してリッスンを開始する

## 1リクエストが来たときの処理順序（server → route → provider）

「リクエストが来たときの流れ」を、実際にコードがどう呼ばれるかのレベルまで分解するとこうなる。

1. サーバーが接続を受け付け、ルーターがpathとmethodから `/v1/chat/completions` のhandlerを選ぶ
2. **その前にauthミドルウェアが必ず先に実行される。** gatewayキーが一致しなければ、handlerには一切進まずここで401を返す
3. handlerがリクエストボディをJSONとして読み込む。壊れたJSONや必須フィールド欠如ならここで400
4. handlerがmodel resolverを呼び、`model`(エイリアス)から実際の`(provider名, 実モデル名)`を引く。エイリアスが`config/models.yaml`に無ければ400
5. handlerがproviders registryから、その`provider名`に対応するadapterを取り出す
6. handlerがadapterの処理を呼び出す。このとき、リクエストが持つ「クライアント切断を検知できる仕組み(context)」も一緒に渡す
7. adapterの中で起きること:
   - リクエストをそのプロバイダーが理解できる形式に変換する(OpenAI宛てならほぼそのまま、Anthropic宛てなら形式変換する)
   - 保管してある本物のAPIキーをヘッダに載せて、上流(OpenAI/Anthropicなど)へ実際にHTTPリクエストを送る
   - **非stream**: レスポンスを最後まで受け取ってから、OpenAI互換の形式に変換してhandlerに返す
   - **stream**: 上流から届くデータを行ごとに読みながら、その都度OpenAI互換のchunk形式に変換して送り返す
8. handlerがadapterの結果をクライアントに書き出す:
   - 非stream: JSONとしてそのまま1回で書く
   - stream: chunkを受け取るたびに`data: {...}\n\n`として書き込み、送信を確定させる。最後に`data: [DONE]\n\n`を書く
9. 途中でクライアントが接続を切った場合、その合図(6で渡したcontext)を使って上流へのリクエストも中断する。中断せずに放置すると、Piがとっくに切断した後も上流への通信・課金が続いてしまう

この9ステップのうち、2(auth)と4(model resolver)は毎回同じ処理、7(adapter)だけがプロバイダーごとに中身が変わる部分。新しいプロバイダーを足すときに触るのは、実質ここだけになる。

## 設定ファイル(config/models.yaml)

```yaml
default_model: pi-default

models:
  pi-default:
    provider: anthropic
    model: claude-sonnet-4-6
  pi-fast:
    provider: openai
    model: gpt-4.1-mini

providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
  anthropic:
    base_url: https://api.anthropic.com/v1
    api_key_env: ANTHROPIC_API_KEY
```

Piは常に `pi-default` のようなエイリアスだけを送ってくる。この対応表を書き換えれば、Pi側の設定は一切変えずに使うプロバイダー/モデルを切り替えられる。

## APIキーの守り方(age)

やることは3つだけ。

1. **鍵を作る** — `age-keygen`で「秘密鍵(identity)」と「公開鍵(recipient)」のペアを1回だけ作る
2. **暗号化する** — 実キーを書いたyamlファイルを、公開鍵で暗号化して`secrets.yaml.age`にする。平文は削除する。この暗号化済みファイルはリポジトリに置いてよい
3. **起動時に復号する** — gatewayの起動時だけ、秘密鍵(identity)を使って`secrets.yaml.age`をメモリ上に復号する。秘密鍵自体はリポジトリに置かず、シークレットマネージャやローカルの`.gitignore`対象ファイルとして別管理する

こうすることで「実キーが書かれた平文ファイル」がディスク上に存在するタイミングを、暗号化する一瞬だけに限定できる。

## エンドポイント

| エンドポイント | やること |
|---|---|
| `POST /v1/chat/completions` | 上記6ステップの本体。streaming/非streamingの両方に対応 |
| `GET /v1/models` | `config/models.yaml`のエイリアス一覧をOpenAI互換の形式で返す(Piがモデル一覧を取得する場合用) |

エラーはすべて `{ "error": { "message", "type", "code" } }` というOpenAI互換の形にそろえる。

## 実装するならこの順番がおすすめ

一気に全部作ろうとすると迷子になりやすいので、小さく動かしながら進めるのがおすすめ。

1. まずは認証もage暗号化もなしで、OpenAIだけに素通しする最小サーバーを動かす
2. gatewayキーの認証(`auth`)を追加する
3. streaming対応を追加する
4. Anthropicアダプターを追加し、`config/models.yaml`の書き換えだけでプロバイダーが切り替わることを確認する
5. 最後に、平文の`.env`キーを`secrets.yaml.age`に置き換える

## 使うライブラリ

- HTTPルーティング: `chi`(軽量で書きやすい)
- YAML読み込み: `gopkg.in/yaml.v3`
- 鍵の暗号化: `filippo.io/age`(ageの公式Go実装)
- ログ: `log/slog`(標準ライブラリ)

## 拡張したくなったら(今回は作らない)

- レート制限・利用量トラッキング
- gatewayキーごとにアクセスできるモデルを制限する
- プロバイダー障害時の自動フォールバック
