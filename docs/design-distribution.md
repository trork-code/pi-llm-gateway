# 配布対応 詳細設計 — `gateway init` ウィザード

「初見ユーザーがダウンロード→最初のチャット成功まで **5分以内**・コマンドは実質 **1つ**」を合格ラインとする、
配布向けウィザード(`gateway init`)の実装設計。方針は MEMD 決定 2026-10-14 を踏襲する。

- 公開CLIは `gateway init`(引数なし `gateway` も同一動作)と `-v` のみ
- 既存サブコマンド(up/status/down/keys/check等)は削除せず、ヘルプ非表示の内部口として温存
- 起動標準は「ログイン時常駐 + ドーマントモード」
- pi extension(JS)は実装しない。配布物は Go バイナリ 1 つ

本書が確定したあと、次の順に実装する(§9 実装バッチ)。

---

## 1. 公開CLIの顔と 4 つの導線

公開コマンドは実質 2 面。ルートヘルプにはこれだけを出す。

```
usage: gateway [init] [-v]

  (引数なし)   初回: セットアップウィザードを開始
               2回目以降: 管理メニューを表示
  init         同上(明示指定)
  -v           バージョン表示
```

| 導線 | きっかけ | 動作 |
|---|---|---|
| ① 裸 `gateway`(主導線) | README / 初見ユーザー | 未セットアップ→確認プロンプト→ウィザード。済みなら管理メニュー |
| ② インストーラ最終メッセージ | goreleaser の zip 同梱 README・リリース文 | 「そのまま `gateway` を実行」 |
| ③ エラーパス案内 | 未セットアップ状態で隠しコマンド(up等)を実行 | 「`gateway setup` not found → `gateway` を実行してください」+ 非ゼロ終了 |
| ④ README 3行クイックスタート | リポジトリ/配布README | `gateway` → ウィザード完了 → piで `/model` |

**未セットアップ判定**: `%USERPROFILE%`(Unix `$HOME`)/`.pi-gateway/setup.json` が無い、または
`config/models.yaml` + `secrets/secrets.yaml.age` のどちらかが欠落、を「未セットアップ」とする。

### 1.1 裸 `gateway` の互換性(重要)

現在の裸 `gateway` は「サーバーをフォアグラウンド起動」である。本設計で裸は init/menu に変わるため:

- 旧動作は隠しサブコマンド **`serve`**(完全互換のフォアグラウンド起動)に移設
- 既存デプロイ(up.cmd は `gateway up` を使用)には影響なし
- systemd/launchd で裸 `gateway` を ExecStart に使う外部デプロイの migration note を README に追記
  (`ExecStart=... gateway serve`)
- **実装上の重要点**: `gateway up` のデタッチspawnが旧来は裸(`exec.Command(exe)`)だったため、
  spawn先は `exec.Command(exe, "serve")` に明示変更する。そうしないと新バイナリで `up` が
  子プロセスとしてウィザードを起動してしまう(local-e2e.sh も bare → serve に更新済み)
- 判定補助: `GATEWAY_SERVE=1` があれば裸でも serve 扱い(レガシー unit の救済)

---

## 2. 対話フロー完全版

### 2.0 共通方針

- 質問は **5 個以内**(プロバイダー → APIキー → モデル/エイリアス → pi登録 → 起動方式)
- 上級設定(identity FILE/env/CMD、SECRETS_DECRYPT_CMD、PORT/BIND等)は質問しない。`init` のフラグ(§2.5)と
  生成物の編集で対応し、doctor で現状が見える
- ウィザード文言は **v1 は英語固定**。Windows 旧 conhost(cp932)で日本語が化けるリスクを排除し、
  i18n は文字列テーブル化して後追い可能にしておく(実装は `internal/wizard/strings.go` に集約)
- Ctrl+C / 各質問で空欄+Enter は「既定値選択」。Esc 的な中断は常に「変更を保存せず終了」
- **age CLI は不要**。identity 生成・暗号化は `filippo.io/age` の Go 実装(`keygen` パッケージ)を
  内製利用する。ユーザーが age を入れる手順は存在しない

### 2.1 初回: ウィザード本体

```
$ gateway
Gateway is not set up on this machine.
Start setup now? [Y/n]            ← n/空Enter以外で終了(変更なし)

[1/5] Which provider do you want to use?
  1) ollamacloud (Ollama Cloud, ollama.com)
  2) openai
  3) openrouter
  4) groq
  5) nvidia
  (anthropic は未実装のため非表示)
  → 環境変数検知: OLLAMA_API_KEY が見つかりました。ollamacloud を使いますか? [Y/n]

[2/5] Provide the API key for ollamacloud.
  Paste the key, or type `env:OLLAMA_API_KEY`, `file:C:\path\to\key`, or `-` then Enter to read stdin.
  > (入力値は即座に内部で検証→暗号化される。平文のままディスクに残らない)
  → 提案: env:OLLAMA_API_KEY が検知済みなら `env:OLLAMA_API_KEY [Y/n]` を既定提示

[3/5] Which model, and under which alias?
  Model [gpt-oss:120b]:          ← プロバイダー別の推奨値を既定提示(§3 表)
  Alias  [pi-default]:           ← 1エイリアス登録。追加は後でメニューから

[4/5] Register the `gateway` provider in pi (~/.pi/agent/models.json)? [Y/n]
  → Y: pi登録コア(piInstall)を実行(§4)。gatewayキーを自動生成して鍵一覧に追加
  → n: gatewayキーを生成するが models.json は触らず、後からメニューで登録できる旨を表示

[5/5] How should the gateway start?
  1) Auto-start at login + dormant mode (recommended)
  2) Start manually with `gateway up`
  → 自動起動: ランチャ生成 + OS別 autostart 登録(§6)

──────────────────────────────────────────────
Setup complete. Summary:
  runtime dir : C:\Users\<you>\.pi-gateway
  provider    : ollamacloud  (key: e563… fingerprint, encrypted)
  alias       : pi-default → gpt-oss:120b
  pi          : registered (models.json updated, 1 backup kept)
  startup     : login auto-start (HKCU\...\Run -> up.cmd), dormant mode on

Gateway is already running (dormant). Try in pi: /model → pi-default
If pi was already open, restart pi to pick up the new provider.
```

- 最後に **稼働確認を自動で行う**: ランチャ経由で `gateway up`(ドーマントで即待受)、`/healthz` で warm:false
  を確認してから完了表示。失敗時は doctor を起動して原因箇所を指す
- 各ステップは **可逆にする**: どの質問でも Ctrl+C で抜けた場合、保存済み手前ステップのみ残す
  (secrets 暗号化済み + 未完了マーカー)。2回目の `gateway` で「setup.json が incomplete → ウィザード再開」
- APIキー検証: 設問 [2/5] で上流 provider に最小リクエスト(GET /models 等)を撃ち 200/401 を確認。
  ネット不通(タイムアウト)なら「skip verification [Y/n]」で先へ進める(オフライン環境対応)

### 2.2 再実行: 管理メニュー

```
$ gateway
Gateway is set up: ollamacloud / 3 aliases / running (warm: idle 12m ago)

  1) Add a provider                     ← §7-1
  2) Replace a provider API key         ← §7-2
  3) Manage models & aliases            ← §7-3 (追加/変更/削除/既定モデル)
  4) Register pi (models.json) again    ← §7-4
  5) Change startup method              ← §7-5
  6) Diagnose (doctor)                  ← §7-6
  7) Rotate secrets                     ← §7-7 (バッチ2実装後)
  q) Quit

> 3
  Models (alias → provider/model, * = default):
    * pi-default  → ollamacloud/gpt-oss:120b
      pi-glm      → ollamacloud/glm-5.3-flash:cloud
  [a]dd / [r]ename / [d]elete / [m]ake default / [q] > a
  Model [e.g. llama3:70b]: …  Alias [pi-llama3]: …
  Applied. `POST /admin/reload` notified → pi: run /model (or restart pi).
```

- メニュー内部の各アクションは **対話ラッパのみ**。実体は CLI コア関数(`internal/ops`)を共有する
  2階建て構成(§7 追加/差し替え対応表)
- 変更系アクション後は常駐への `/admin/reload` を自動通知(失敗時は「反映には再起動」ヒント)。
  `keys add -reload` 相当の既存経路を再利用する
- pi側への反映は **models.json の再マージ(alias一致)** が `4)` のときだけ必要。`2)/3)` は pi 側無変更

### 2.3 非対話モード

CI・agent 組み込み向け。プロンプトを一切出さず、欠けはフラグ既定で埋めて成功させる。

```
gateway init --provider ollamacloud --api-key env:OLLAMA_API_KEY --yes
```

| フラグ | 意味 | 既定 |
|---|---|---|
| `--provider NAME` | §3 のprovider id | 必須(`--yes`時) |
| `--api-key SRC` | 値 / `env:NAME` / `file:PATH` / `-`(stdin) | 必須 |
| `--model M` | 実モデル名 | §3 の推奨値 |
| `--alias A` | エイリアス名 | `pi-default` |
| `--pi` / `--no-pi` | models.json 登録/スキップ | `--pi` |
| `--startup resident\|manual` | 起動方式 | `resident` |
| `--yes` | 全確認を自動承認(プロンプト無効) | なし |
| `--port N` / `--bind ADDR` | PORT / BIND | 18080 / 127.0.0.1 |
| `--identity-file P` / `--identity-env N` / `--identity-cmd C` | identity 3系統(排他) | 自動生成(ファイル) |
| `--secrets-decrypt-cmd C` | SECRETS_DECRYPT_CMD 委譲 | なし |
| `--runtime-dir DIR` | 配置先上書き | `~/.pi-gateway` |
| `--gateway-key ENV\|file\|-` | 既存gatewayキーを流用(再セットアップ時) | 自動生成 |

- `--api-key file:` / stdin は CI でも履歴に残らない経路(推奨は `env:` または `-`)
- 既存セットアップ済み環境に `--yes` で実行した場合: メニュー相当の **冪等 upsert** として扱い、
  既存 gatewayキー・identity は上書きしない(§4.3 のキー保全規則を適用)
- 成功時に機械可読サマリを `--print-json` で出せる(インストーラ/E2Eが利用)

### 2.4 状態検知と提案(env 検知)

起動時に既存環境変数を走査し、プロバイダー選択とキー入力の既定に反映する。

| env | 提案先 |
|---|---|
| `OPENAI_API_KEY` | provider=openai, key=env:OPENAI_API_KEY |
| `OLLAMA_API_KEY` | provider=ollamacloud …以下同様 |
| `OPENROUTER_API_KEY` / `GROQ_API_KEY` / `NVIDIA_API_KEY` | 各 provider |
| `ANTHROPIC_API_KEY` | **提案しない**(未実装。doctor が「将来対応」と表示) |

複数検知時は「候補リスト」を示すが、推奨 1 つを既定に据える(順序: 上表の上から)。

---

## 3. provider カタログ(ウィザード既定値のsource of truth)

`internal/wizard/catalog.go` に集約する定数表。メニュー・doctor ともこれを参照。

| provider | base_url(固定) | env(GW検知) | 推奨モデル例 | pi既定メタ |
|---|---|---|---|---|
| `ollamacloud` | `https://ollama.com/v1` | `OLLAMA_API_KEY` | `gpt-oss:120b` | cw 131072 / text / reasoning |
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `gpt-4.1-mini` | cw 128000 / text+image / reasoning |
| `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `meta-llama/llama-3.3-70b-instruct` | cw 131072 / text / reasoning |
| `groq` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | `llama-3.3-70b-versatile` | cw 131072 / text / reasoning |
| `nvidia` | `https://integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` | `meta/llama-3.1-405b-instruct` | cw 131072 / text / reasoning |

- 推奨モデル・pi既定メタは「入力欄の既定値」であり上書き可能。メタ上書きは `models.yaml` の
  alias エントリに `pi: {contextWindow, input, reasoning}` を書けば反映(§4.4)
- モデル名のtag必須(ollamacloud `gpt-oss` は上流404)等の注意は、プロンプト直下に1行で出す
- `anthropic` は catalog に定義のみ持たせ、ウィザード/メニューでは disabled 表示
  (`(not yet implemented)`)。501スタブのまま

---

## 4. `models.json` マージ仕様(`pi install` コア)

### 4.1 対象ファイルとフォーマット

- パス: `%USERPROFILE%\.pi\agent\models.json`(Unix: `~/.pi/agent/models.json`)
- pi本体が存在しないマシンでも動作: ディレクトリが無ければ作り、`{"providers":{"gateway":{…}}}` のみの
  最小 json を新規作成する(pi後入れインストールでもそのまま読める)
- 書式: `providers` オブジェクト直下の `gateway` 1ブロックのみを gateway 所有権とする。
  他の provider(ollama 等)・他トップレベルキーは **一切触らない**

### 4.2 生成するブロック

```json
"gateway": {
  "api": "openai-completions",
  "apiKey": "gk-…(gatewayキー)",
  "baseUrl": "http://127.0.0.1:18080/v1",
  "models": [
    { "id": "pi-default", "_launch": true, "reasoning": true,
      "contextWindow": 131072, "input": ["text"] }
  ]
}
```

- `baseUrl` のHOSTは `http://127.0.0.1:<PORT>/v1`(TLS構成時は `https://…` + 証明書警告の注意喚起)。
  PORT は `setup.json` に記録した値(§5)
- `models` は **config/models.yaml の全エイリアス**から機械生成する(手動との drift をゼロにする)

### 4.3 キー保全・バックアップ規則

1. 既存 `gateway` ブロックがあり `apiKey` が `gk-` 接頭で secrets の gateway_keys に実在 → **そのキーを流用**
2. 流用不可(欠落/不一致/初回)→ `keys add` コアで新規ランダム生成(`gk-`+128bit)して block に書く
   (既存値が `gk-` 以外=ユーザー手入力なら、確認プロンプトを出してから置換)
3. 書き込み前に **タイムスタンプ付きバックアップ**: `models.json.bak-<YYYYMMDD-HHMMSS>` を作り、
   直近 **3世代**まで残す(古いものを削除)。現在の `.bak` 慣行を上書き規則として標準化
4. 一貫性: 一度のマージは読み込み→(バックアップ)→unmarshal(map)→gatewayブロック差し替え→marshal(2space indent)→
   同一ディレクトリへ temp file → rename(原子的差し替え)。`keys set` の Save と同型の書き戻し方式

### 4.4 alias → models.json のメタ生成

エイリアスごとに:

- `id`: エイリアス名
- `contextWindow`/`reasoning`/`input`: 対応 provider のカタログ既定(§3)。`models.yaml` に
  `pi:` ブロックがあればそちら優先
- `_launch: true` は常に付与

### 4.5 `pi uninstall`(メニュー対・非公開CLI)

- `gateway` ブロックのみ削除。残 provider 無しでもファイルは残す(空 providers)
- 「直近バックアップから復元しますか? [y/N]」を対話モードのみ提示

### 4.6 doctor との連携

- `models.json` 内 `gateway.models[].id` と `models.yaml` エイリアスの **差分検査**を doctor に追加
  → drift は `[warn] pi models.json: alias drift (pi-glm missing) → run: gateway init (4)`

---

## 5. ディレクトリ配置(配布時の配置規約)

単一 runtime dir に全収容。配布バイナリは **自分自身を runtime dir へコピー**する
(後から PATH やインストール場所が変わってもランチャが壊れない)。

```
~/.pi-gateway/                     (Windows: %USERPROFILE%\.pi-gateway)
├── setup.json        ← 状態マーカー(非秘匿): version/provider群/aliases/startup/port/作成日
│                        init 再実行判定と doctor の入力。ここにキー値は絶対に書かない
├── bin/gateway(.exe) ← 自己コピー。ランチャは必ずこのパスを呼ぶ
├── config/models.yaml
├── secrets/identity.txt         0600 owner-only(initが内部keygen)
├── secrets/secrets.yaml.age     0600 owner-only
├── gateway.log
└── up.cmd / up.sh / up.fish     ← OS別ランチャ(§6)
```

- Windows 以外でも dir 名は `.pi-gateway` に統一(SETUP.md 実運用と同じ形を init が自動生成する)
- `--runtime-dir` で配置変更可。移設手順は「`down` → dir移動 → `gateway` (メニュー5でランチャ再生成)」
- 既存実運用(`.pi-gateway` 手動構築)との互換: `setup.json` が無い既存環境は「未セットアップ」ではなく
  **「config+secrets が揃う → セットアップ済みだが setup.json が無い = 初期化して同期」**(メニュー表示)。
  既存 gatewayキー/piキーは保持する

### 5.1 PORT 既定の決定

- ウィザード生成物は **PORT=18080** を既定にする(up.cmd 内 env)。既存実運用と一致し、
  他開発ツールの 8080 と衝突しない。サーバー側の「env未指定時 8080」規則は変更しない(上流互換)
- 衝突検知: init が 18080 を占有する他プロセスを見つけたら `--port` 提案(+setup.json と models.json
  の baseUrl に反映)

---

## 6. 起動方式(autostart + ランチャ)

起動方式で生成するもの(バッチ3で3OS実装。バッチ1では `2) manual` 相当の生成のみ):

| OS | 方式 | ランチャ | 登録先 |
|---|---|---|---|
| Windows | HKCU Run | `up.cmd`(env設定 + `bin\gateway.exe up -log …` + status表示) | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` の `pi-gateway` 値 |
| macOS | LaunchAgent | `up.sh` | `~/Library/LaunchAgents/dev.pi.gateway.plist`(RunAtLoad) |
| Linux | systemd --user | `up.sh` | `~/.config/systemd/user/pi-gateway.service` + `--now` |

- ランチャは冪等(既存実装 `gateway up` の性質をそのまま利用: 稼働済みなら nothing)
- ドーマントモード(§8)が標準。プロセスはログイン直後に常駐し 127.0.0.1:<port> で待受、
  鍵は初回リクエスト時に復号
-解除: メニュー 5) で manual 選択 → Run値/plist/unit を除去(登録だけ消し、ランチャファイルは残す)
- 上級: `--startup-args` で EXTRA 環境変数群(EGRESS_ALLOWLIST 等)をランチャに焼き込むフラグ

---

## 7. メニュー項目 → コア関数対応表(2階建て構成)

UI(対話)とコア(内部CLI)はコードレベルで統合。コアはすべて `internal/ops` パッケージへ段階移管する
(既存 `keysCmd`/`serviceCmd` はラッパ化)。

| # | メニュー項目(対話) | コア関数(共有) | 既存資産の流用 |
|---|---|---|---|
| 1 | プロバイダー追加 | `ops.ProviderAdd`(yaml upsert + `secrets set` + alias質問へ接続) | keys set / config書込を組合せ、reload 通知まで |
| 2 | APIキー差し替え | `ops.ProviderKeySet`(= keys set の薄ラッパ) | keysCmd実装を移管 |
| 3 | エイリアス管理 | `ops.AliasUpsert / AliasDelete / AliasDefault`(models.yaml 編集 + reload) | 新規(config core) |
| 4 | pi再登録 | `ops.PiInstall / PiUninstall` | 新規(§4)。非公開CLI `pi install` も同じコア |
| 5 | 起動方式変更 | `ops.AutostartEnable/Disable`(§6)+ `ops.LauncherWrite` | 新規。手動→Run値除去も同一関数 |
| 6 | 診断 | `ops.Doctor` | 新規(§10)。非公開CLI `doctor` も同一コア |
| 7 | secretsローテーション | `ops.RotateSecrets` | scripts/rotate-secrets.sh の Go 化(バッチ2) |
| — | (起動/停止/状態) | `serviceCmd` コア既存 | up/status/down は隠しCLIのまま、メニューからも呼ぶ |

- 非公開CLI(`gateway pi install|uninstall`, `doctor`, `models`, `secrets rotate`)はメニューと同一コアを
  呼ぶ「2階目の窓口」。方針「公開は init 1つ」を崩さないためルートヘルプには出さない
- 全コア関数は `-quiet` モードを持ち、メニューは進行表示、CLI は静かに出し分ける

---

## 8. ドーマントモード(設計確定パラメータ)

**目的**: ログイン常駐でも、鍵を復号せずにメモリ最小化しておき、必要になった瞬間だけ温める。
これにより「指示を投げた時点から使える」(ECONNREFUSED 原理消滅)を extension なしで成立させる。

### 8.1 状態機械

```
起動: binロード → BIND/PORT待受OK → ここで止まる(復号しない) = COLD
  │  GET /healthz は常時応答(warm:false)。復号のトリガにはならない
  ▼
リクエスト到達(POST /v1/* / GET /v1/models / POST /admin/*)
  │  IPがrate-limitブロック済み → 復号せず429(復号DoS予防)
  │  singleflightで1回だけ復号(同時多発リクエストは待ち合流)
  │  復号+load budget: 10秒(超過は500 fail-closed)。ローカルage復号は実質<100ms
  ▼
WARM → 以後は現行動作(auth→resolver→adapter)。STRICT_KEYS 検査は warm 完成時に実施
  │
  │  認証後トラフィックが連続 IDLE_ZERO_MINUTES 無し + in-flight 0
  ▼
COLD へ戻す(Secrets.Zero() は in-flight 完了後)。以後のリクエストは再び warm から
```

| パラメータ | 既定 | 出典 |
|---|---|---|
| 復号待ち budget | 10s(固定) | 設定項目にしない(ローカル定数) |
| `IDLE_ZERO_MINUTES` | **30**(0=Zero化しない) | 環境変数。ランチャは既定値を焼き込む |
| トリガ対象 | `/v1/*`, `/admin/*`(`/healthz`とTLS握手は対象外) | 実装定数 |
| `/healthz` 表示 | `"warm": bool, "warm_since": RFC3339\|null, "idle_zero_minutes": n` | 追加フィールド |
| 復号に passphrase が必要な構成 | **ドーマント不適合**→起動時に通常ロードへフォールバック+ログ警告 | 判定は起動時に identity 形式から推定(`keygen` が `-p` 形式検出可能) |
| `AGE_IDENTITY_CMD`/env | ドーマント適合(非対話なら) | 毎回 warm で呼ばれる |

### 8.2 pi側の再試行問題(MEMD未決事項の結論)

- 常駐+ドーマントにより、piが接続試行する時点でポートは開いている。piのECONNREFUSED時自動再試行の
  有無は非ブロッキング(実害が原理的に消える)。何もしない
- warm 化遅延は pi のHTTPタイムアウトより十分短い(ageローカル復号)ため、pi側設定変更不要

### 8.3 実装の置き場所

- `handlers.Runtime` スナップショット機構(atomic.Pointer)を前提に、warm化ゲートを auth/handler 直前に
  1点挿入(singleflight付き)。`Runtime=nil`(cold)状態のスナップショットを許す形に拡張
- `gateway keys`/`-check` は cold 常駐にも `resolveSecretsForCLI` で対話できる(変更なしの性質)

---

## 9. 実装バッチ(確定)

| バッチ | 内容 | 検収要件 | 検証手段 |
|---|---|---|---|
| **第1** | `init` ウィザード + `pi install/uninstall`(§2,4,5 のうち setup.json/config/secrets 生成・models.jsonマージ・自己コピー)。メニューは初期形(1/2/4/q)のみ | 初見ユーザーが `gateway` 1回で pi から最初のモデル送信に到達 | 新規 `scripts/local-init-e2e.sh`(非対話フラグで全パターン)+ Windows手動 |
| **第2** | doctor + models + secrets rotate のGo化 | doctor が10項目を出す。rotate が Windows で fail-closed に完走 | doctor 単体テスト + rotate ラウンドトリップ |
| **第3** | autostart 3OS + **ドーマントモード** + goreleaser | ログイン直後から1発リクエストが通る。goreleaser zip で `gateway` が回る | 手動(ログオフ/オン)+ CI build matrix |

- 第1時点の自動起動は「manual」既定(=今日の `gateway up` と同等)。ドーマントは第3で ON
  → 第1リリースは「セットアップ自動化」、第3で「常駐の完成形」とするので初期配布でも壊れない

---

## 10. doctor の検査項目(バッチ2の詳細先行確定)

1. runtime dir / setup.json 存在・整合
2. `config/models.yaml` parse + alias→provider 参照整合 + 既定モデル存在
3. secrets 復可(identity経由) + 鍵齢(SECRETS_MAX_AGE_DAYS)
4. gateway_keys 数と pi models.json の apiKey 一致
5. pi models.json の alias drift(§4.6)
6. 稼働状態: `/healthz`(warm, uptime, version) 参照。**cold 常駐は正常として表示**(warnにしない)
7. 自動起動登録状態(§6 各OSの実体検査)
8. ポート競合 / BIND(127.0.0.1 固定推奨)
9. バイナリ: runtime `bin/` と実行中CLIのバージョン差(アップデート忘れ検知)
10. Windows: console codepage(chcp)が 65001 以外の際の表示注意 + cp932対策済みである旨

出力: `[ok] / [warn] / [fail]` 行 + 末尾に「fix 提案」(該当メニュー番号 or フラグ1行)。
exit code は fail>0 → 1, warn>0 → 0(スクリプトから使えるように)。

---

## 11. 隠しサブコマンド最終一覧(契約面)

ルートヘルプに載せない。動作は保守され、CI/E2E/ランチャの契約として維持する。

| コマンド | 用途 | 出所 |
|---|---|---|
| `up` / `status` / `down` | ランチャ・E2E用の起動三種 | 既存(b614d5a) |
| `keys list/add/remove/set/unset` | secrets 編集 | 既存(edfcfd5) |
| `-check` | 起動前検証 | 既存 |
| `serve` | 旧・裸gateway相当のフォアグラウンド起動(§1.1) | 新設(移設) |
| `pi install` / `pi uninstall` | models.json マージ/解除 | 新規(バッチ1) |
| `doctor` | 診断のCLI口 | 新規(バッチ2) |
| `models` | エイリアス表表示(現在のconfig基準) | 新規(バッチ2) |
| `secrets rotate` | rotate-secrets.sh の Go 化(Windows対応) | 新規(バッチ2) |
| `version` / `-v` | バージョン | 既存(`-v`のみ公開面に表示) |

削除はしない。新規コマンドも全て「ヘルプ非表示・動作あり」で統一する。

---

## 12. 未決→決定の確定まとめ

| 議題 | 決定 |
|---|---|
| piのECONNREFUSED再試行 | 非ブロッキング(ドーマント常駐で実害消滅。何もしない) |
| ドーマント復号の最大待ち | 10s 固定(設定化しない) |
| アイドルZero化の既定 | 30分(`IDLE_ZERO_MINUTES`, 0=無効) |
| ウィザード言語 | v1 は英語固定(strings テーブル化で後追いi18n可) |
| age CLI 依存 | なし(`filippo.io/age` 内製利用で keygen/暗号化完結) |
| 配布ディレクトリ | `~/.pi-gateway` に全収容+自己コピー+setup.json |
| bare `gateway` の旧動作 | 隠し `serve` に移設(+`GATEWAY_SERVE=1` レガシー救済) |
| models.json バックアップ | タイムスタンプ付き、3世代保持 |
| gatewayキーの保全 | 既存 `gk-` キーは流用、新規時のみ生成 |
| PORT 既定(ウィザード) | 18080(server のenv既定8080は不変) |

---

## 13. 留意(設計外・実装時に確認)

- piが `models.json` をセッション内で再読込するか(/reload の効用)は要実機確認。確認まで
  「pi を再起動して反映」と案内する
- goreleaser の署名(Windows: none, macOS: ad-hoc)は第3バッチで決める
- `setup.json` に書くフィールド確定(草案: version, providers[], aliases[], port, startup, identity_mode,
  created_at, updated_at)
- `internal/ops` への移管は既存テスト(keys_cmd_test/service_cmd_test/main_test)を壊さないよう、
  ラッパ化は署名維持で段階的に