# secrets/

実APIキー一式を格納するディレクトリ。

- `secrets.yaml.age` — 実キーを書いたyamlを **ageで暗号化したもの**。この暗号化済みファイルはリポジトリに置いてよい
- 平文の `secrets.yaml` や ageの秘密鍵(identity)は **絶対に置かない**（`.gitignore` 対象）

## 運用手順

`secrets.yaml`（平文、暗号化後に削除）の形式：

```yaml
gateway_key: <Pi側で使う合言葉>
gateway_keys:            # 複数キーも可(将来のキーごとアクセス制御用)
  - <別の合言葉>
api_keys:
  openai: sk-...
  anthropic: sk-ant-...
  ollamacloud: <ollama.comのAPIキー>
```

```bash
# 1. 鍵ペアを作る。秘密鍵はリポジトリ外で管理する
#    (推奨) パスフレーズ保護付きで作る — 平文の鍵がディスクに落ちない
#    ※ age-keygenの公開鍵行はstderr/コメントで表示され、identity内容には含まれない
age-keygen | age -p -o identity.age

# 2. 実キーを書いたyamlを暗号化する(recipientはidentity作成時に表示された公開鍵)
age -r <RECIPIENT> -o secrets.yaml.age secrets.yaml

# 3. 平文を削除する
rm secrets.yaml
chmod 600 secrets.yaml.age identity.age

# 4. gateway起動時に復号する
AGE_IDENTITY_FILE=/path/to/identity.age \
AGE_PASSPHRASE=<パスフレーズ> \
./gateway
```

## 多層防御との対応

- **① 保管時** — `secrets.yaml.age`のみをコミット。identityは`age -p`でパスフレーズ保護推奨（鍵ファイルを盗まれても復号できない）
- **② 鍵の受け渡し** — 秘密鍵はファイル以外に`AGE_IDENTITY`環境変数（シークレットマネージャから起動時に注入）でも渡せる
- **③ 権限** — identityは所有者のみ（0600）。gatewayはrootではなく専用ユーザーで実行する
- **⑨ ローテーション** — `bash scripts/rotate-secrets.sh <RECIPIENT>` で復号→編集→再暗号化が一発でできる