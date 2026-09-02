# secrets/

実APIキー一式を格納するディレクトリ。

- `secrets.yaml.age` — 実キーを書いたyamlを **ageで暗号化したもの**。この暗号化済みファイルはリポジトリに置いてよい
- 平文の `secrets.yaml` や ageの秘密鍵(identity)は **絶対に置かない**（`.gitignore` 対象）

## 運用手順

`secrets.yaml`（平文、暗号化後に削除）の形式：

```yaml
gateway_key: <Pi側で使う合言葉>
api_keys:
  openai: sk-...
  anthropic: sk-ant-...
  ollamacloud: <ollama.comのAPIキー>
```

```bash
# 1. 鍵ペアを作る（1回だけ。秘密鍵はリポジトリ外で管理）
age-keygen -o identity.txt

# 2. 実キーを書いたyamlを暗号化する（recipientはidentity.txtに表示された公開鍵）
age -r <RECIPIENT> -o secrets.yaml.age secrets.yaml

# 3. 平文を削除する
rm secrets.yaml

# 4. gateway起動時に復号する
AGE_IDENTITY_FILE=/path/to/identity.txt ./gateway
```