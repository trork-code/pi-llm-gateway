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
  openrouter: sk-or-...       # openrouter.aiのAPIキー
  groq: gsk_...               # console.groq.comのAPIキー
  nvidia: nvapi-...           # build.nvidia.comのAPIキー
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

# 4. gateway起動時に復号する（鍵ソースは3種類。**1つだけ**指定すること）

```bash
# (a) ファイルから(権限は所有者のみ必須)
AGE_IDENTITY_FILE=/path/to/identity.age AGE_PASSPHRASE=<パスフレーズ> ./gateway

# (b) コマンド実行で取得(シークレットマネージャ連携。鍵がディスクに常駐しない)
AGE_IDENTITY_CMD="vault kv get -field=identity secret/pi-llm-gateway" \
  AGE_PASSPHRASE=<パスフレーズ> ./gateway
AGE_IDENTITY_CMD="aws secretsmanager get-secret-value --secret-id pi-gateway --query SecretString --output text" ./gateway

# (c) 環境変数に直接注入(シークレットマネージャがプロセス環境を制御できる場合)
AGE_IDENTITY=<秘密鍵の内容> ./gateway

# (d) 復号自体をage CLIに委譲(SECRETS_DECRYPT_CMD)
#     YubiKeyプラグイン等、「鍵の実体がハードウェアに留まる」運用に対応
SECRETS_DECRYPT_CMD="age -d -i /path/to/yubikey-identity.txt secrets/secrets.yaml.age" ./gateway
```

## identity(秘密鍵)のローテーション（多層防御⑨）

```bash
# 1. 新しいidentityを作る(パスフレーズ付き推奨。新recipientはstderrに表示される)
age-keygen | age -p -o identity2.age

# 2. secrets.yaml.ageを新recipientへ再暗号化
#    (現在のidentityでは新ファイルを復号できないため --skip-verify が必要)
bash scripts/rotate-secrets.sh --skip-verify <新recipient>

# 3. 新しいidentityで復号できることを確認してからgatewayを再起動
AGE_IDENTITY_FILE=identity2.age AGE_PASSPHRASE=... ./gateway

# 4. 旧identityを失効/削除する
```

## 複数recipient（「決まった環境の鍵でしか復号できない」ように絞る: 多層防御②）

```bash
# 環境ごとのrecipientを並べて暗号化(1つの鍵で全環境が復号できる状態を作らない)
age -r <env1-recipient> -r <env2-recipient> -o secrets.yaml.age secrets.yaml

# ローテーション時も複数指定可
bash scripts/rotate-secrets.sh <env1-recipient> <env2-recipient>
```

## 多層防御との対応

- **① 保管時** — `secrets.yaml.age`のみをコミット。identityは`age -p`でパスフレーズ保護推奨（鍵ファイルを盗まれても復号できない）
- **② 鍵の受け渡し** — 秘密鍵はファイル/環境変数(`AGE_IDENTITY`)/コマンド実行(`AGE_IDENTITY_CMD`)の3系統。コマンド実行ならVault/AWS/GCP等から起動の瞬間だけ取得でき、鍵がディスクに常駐しない。重複指定は起動拒否。recipientを複数指定し「決まった環境の鍵でしか復号できない」構成も可
- **③ 権限** — identityは所有者のみ（0600）。gatewayはrootではなく専用ユーザーで実行する
- **⑨ ローテーション** — `bash scripts/rotate-secrets.sh [--skip-verify] <recipient>...` で復号→編集→再暗号化→復号ラウンドトリップ検証→バックアップ退避が一発でできる。identity自体のローテーションも上記の手順で対応