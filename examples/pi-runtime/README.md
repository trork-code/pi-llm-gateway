# pi-gateway 常駐ランタイム例(pi coding agent接続)

実運用の常駐ディレクトリの例。`bin/gateway.exe` 本体と、このフォルダの2ファイルを任意の場所に置く。

```
<runtime-dir>/
  bin/gateway.exe        go build -o bin/gateway.exe ./cmd/gateway
  config/models.yaml     このフォルダの models.yaml(エイリアス定義。非秘匿)
  up.cmd                 このフォルダの up.cmd.example を改名(秘値を含まない)
  secrets/               identity.txt + secrets.yaml.age(READMEのクイックスタートで作成)
  gateway.log            up が追記するサーバーログ
  pikey.txt              pi用gatewayキーの控え(秘値。例に含めない)
```

## up.cmd.example

- 起動に必要な env(PORT/BIND/CONFIG_FILE/SECRETS_FILE/AGE_IDENTITY_FILE)をこのディレクトリに閉じ込める
- `gateway up`(冪等)+ `gateway status` で最後に状態表示
- Windowsスタートアップ(HKCU Run)やタスクスケジューラに登録してログイン時自動起動にできる

## 動作確認

```bash
# 疎通(認証不要)
curl http://127.0.0.1:18080/healthz

# 実リクエスト(pi キー)
KEY=$(cat pikey.txt)
curl -s -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"pi-ollama-120b","stream":false,"messages":[{"role":"user","content":"ping"}]}' \
  http://127.0.0.1:18080/v1/chat/completions
```

- pi側の接続設定(provider宣言)はリポジトリ README の「pi coding agent との接続」を参照
- 鍵の追加/差し替えは `gateway keys add|set ... -reload`(README「鍵の管理CLI」)