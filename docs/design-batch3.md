# Batch 3 詳細設計 — autostart 3OS / ドーマント / goreleaser

Status: design (impl follows). Base: `docs/design-distribution.md` §6/§8/§9/§10 の確定分 +
**PR#1 `docs/proposal-startup.md`(全面採用・2026-11-11)** のプロセス契約。実装済み前提:
runtime dir 自己解決(`internal/setup.Discover`、3cc31ab)。

## 1. 目標とプロセス契約(PR#1 確定分の要約)

- 常駐は **install あたり `gateway serve` フォアグラウンド 1 プロセス**。`up` は detach spawn をしない。
- ログイン起動は **OS が serve を直接実行**(登録コマンドラインに up.cmd/up.sh を含めない)。
- バイナリ自身が install を発見する(3cc31ab で済): config/secrets/identity は標準配置から、
  PORT/BIND は `PORT env > setup.json > ランチャ解析 > 8080`。env 明示常に優先。
- 既存 `up.cmd/up.sh` は **1 リリース残して互換を保ちつつ、新規登録/新規生成は依存しない**。
- `GATEWAY_SERVE=1`(旧裸経路)はレガシー救済として維持。

### 1.1 up の新仕様(このバッチで変更)

```
gateway up
  1. resolveGatewayURL(発見) で /healthz を probe
     稼働 → READY 表示、exit 0(dormant 中も READY 表示=dormant:true を併記)
  2. 未稼働 → OS 登録の有無を確認(§2)
     登録あり → OS API で発火して起動を依頼(systemctl --user start / launchctl kickstart /
       schtasks /run)。短時間 probe で READY 待ち(同 §3 の budget)。exit 0
     登録なし → 「起動していません。`gateway serve`(フォアグラウンド)または
       `gateway autostart install`(常駐化)で起動してください」を表示し exit 1
  3. fork(detach spawn)は完全撤去。spawn_windows.go / spawn_unix.go は serve 起動経路から削除
     (ファイル自体は admin/reload 通知や将来用途のために一旦残すが、up からは呼ばない)
```

外部から spawn される serve が消えるため、Windows job object の kill-on-close 問題
(親消滅で子道連れ)と「8080 迷子サーバー」が原理的に消える。

### 1.2 ランチャ(up.cmd/up.sh)の新形式(互換維持)

init ウィザードは **env 割当てを書かない**新形式のランチャのみ生成(旧形式の上書きはしない。

```
up.cmd(Windows)
  @echo off
  rem legacy manual-start helper; autostart registers `gateway serve` directly.
  cd /d "%~dp0"
  start "" /b "%~dp0bin\gateway.exe" serve
  timeout /t 1 >nul
  "%~dp0bin\gateway.exe" status

up.sh(unix)
  #!/bin/sh
  # legacy manual-start helper; autostart registers `gateway serve` directly.
  cd "$(dirname "$0")"
  ./bin/gateway serve &
  sleep 1
  ./bin/gateway status
```

- ログイン起動はこのランチャを経由しない(OS 登録=serve 直接)。ランチャは手動起動の便宜物のみ。
- 旧形式ランチャとの非互換は無い(旧形式は env を渡して serve が env 優先で動く=引き続き動く)。

## 2. autostart コマンド(3OS)

`gateway autostart install|uninstall|status`(メニュー5と同一コア `ops.Autostart*`)。

- 登録内容は **OS 共通で `<runtime>/bin/gateway(.exe) serve` の絶対パス 1 行**。
  PORT/BIND/identity は焼かない(serve が Discovery で解決: PR#1)。
- `--runtime-dir` で明示可(既定は ResolveDir)。bin が無い場合は `SelfCopy` 相当のコピーをしてから登録。
- `setup.json` がある場合のみ `startup: "resident"` を書く(manual 環境に autostart install を
  打った場合も resident に更新する=ドーマントが有効になる旨を表示)。
- **dry-run**: `--print` で登録定義を stdout へ出力のみ(E2E/CI 用。OS への副作用なし)。
- uninstall は登録だけ除去(ランチャ/鍵/設定は触らない)。

### 2.1 OS 別仕様

| OS | supervisor | 登録先 | 発火(up 用) | 解除 |
|---|---|---|---|---|
| Linux | systemd --user | `~/.config/systemd/user/pi-gateway.service`(Type=simple, `ExecStart=<abs> serve`, `Restart=on-failure`, `RestartSec=3`) | `systemctl --user start pi-gateway` | `disable --now` + unit 削除 + daemon-reload |
| macOS | LaunchAgent | `~/Library/LaunchAgents/dev.pi.gateway.plist`(ProgramArguments=[`<abs>gateway`, `serve`], RunAtLoad+KeepAlive) | `launchctl kickstart -k gui/$UID/dev.pi.gateway` | `bootout gui/$UID/dev.pi.gateway` + plist 削除 |
| Windows | Task Scheduler(ログオントリガー) | タスク `pi-gateway`(`schtasks /create /tn pi-gateway /xml <file>`)。XML は `<LogonTrigger>` + `<Command>=<abs>gateway.exe</Command><Arguments>serve --winhide</Arguments>` | `schtasks /run /tn pi-gateway` | `schtasks /delete /tn pi-gateway /f` |

- Windows の Task Scheduler 採用理由(PR#1 §Windows): HKCU Run は手動発火手段が無い(logon のみ)ため
  `up` の OS 発火が成立しない。Task Scheduler は `schtasks /run` で発火でき、コンソール表示は
  `--winhide` で隠す。restart-on-fail は将来のオプション(初版は RestartCount を設定しない)。
- **`--winhide`(Windows 専用新フラグ)**: serve 起動直後に自プロセスの `GetConsoleWindow` を
  `ShowWindow(SW_HIDE)` する(Task Scheduler から起動した場合にコンソールが点滅しない)。
  手動のフォアグラウンド serve(ターミナル)ではフラグ無し=通常表示。build tag で分離
  (`winhide_windows.go` / `winhide_other.go`)。
- 既存の旧登録(up.cmd を参照する HKCU Run 値等)は install 時に検出して **置換**(旧値は削除して
  新登録を書く。削除前に旧値文言を note 表示)。旧ランチャファイルは残す(1 リリース互換)。
- 権限: 全てユーザープロファイル内(schtasks は `<RunLevel>LeastPrivilege</RunLevel>`)。
  ユーザー同意を求める対話は install 時に1回表示(manual)。

### 2.2 status 出力

`gateway autostart status`: 各 OS の登録実体(存在/コマンドライン/Enabled)+PATH 組込状態(§4)。
doctor item7 はこの共通検査を呼ぶ形に統合。

## 3. ドーマントモード(§8 確定分の実装+PR 互換の修正点)

§8 の状態機械(COLD/WARM、singleflight、budget 10s、Rate-limit は cold でも効く、healthz は
warm トリガ外)をそのまま実装する。PR 採用による変更点のみ:

| 項目 | §8 原案 | バッチ3 確定 |
|---|---|---|
| dormant 判定 | ランシャが env/引数で制御 | **setup.json `startup=="resident"` のとき serve が dormant で起動**。override は `--dormant`/`--no-dormant` と env `GATEWAY_DORMANT` |
| `IDLE_ZERO_MINUTES` | ランチャが焼き込む | ランチャ引退に伴い **env 未指定時の既定 30**(`IDLE_ZERO_MINUTES=0`=Zero 化しない)。ランチャ焼き込みは廃止 |
| OS 登録時の制御 | — | 登録コマンドラインは serve 固定。ドーマントの on/off は setup.json の startup で決まる(登録に焼かない) |

- warm のトリガ: `/v1/*` と `/admin/reload`、`/admin/shutdown`(down は warm→認証→停止の順)。
  `/healthz` と TLS 握手はトリガ外(§8)。Rate-limit(IP 失敗カウンタ)は cold のまま判定。
- in-flight と Zero: 認証済みリクエストの完了時刻を記録し、`IDLE_ZERO_MINUTES` 経過かつ
  in-flight 0 で COLD へ swap(`old.Secrets.Zero()`)。1 分粒度 ticker、hot reload との競合は mutex。
- COLD の Runtime: `keysFn` が空配列を返す(全認証リクエストが 401)。dormant 中の pi からの
  初回リクエストは warm ミドルウェアが復号→通常 auth へ流すため体感遅延は age 復号分(<100ms 程度)。
- `/healthz` に `dormant: bool`(既存フィールドは不変)。doctor item6 は warm/cold/dormant を区別表示
  (dormant は正常)。
- 稼働ログ・audit: `dormant_warm`(所要)と `dormant_zero`(鍵値は出さない)を追記(§既存経路)。
- passphrase 付き identity / `SECRETS_DECRYPT_CMD`: §8 の適合表どおり(passphrase 付きは dormant
  不適合=起動時に通常ロードへフォールバック+警告)。DECRYPT_CMD は毎 warm で呼ばれる(適合)。

## 4. ユーザー PATH 組込(Windows のみ)

- `gateway autostart install --path`(および uninstall): `HKCU\Environment\Path` へ
  `%USERPROFILE%\.pi-gateway\bin` を追加/除去。**setx を使わない**(既存値のトランケート防止)。
  書込み後 `SendMessageTimeout(HWND_BROADCAST, WM_SETTINGCHANGE, "Environment")` を送る
  (x/sys/windows)。既存 Path の REG タイプ(REG_SZ/REG_EXPAND_SZ)は保持。
- 重複(末尾一致 or 拡張なし完全一致)は skip+表示。unix は PATH 組込をしない(install 出力で
  `export PATH=…` の案内のみ)。
- `autostart status` に PATH の組込状態を表示。doctor item7 に統合。

## 5. goreleaser(配布)

- `.goreleaser.yml`: builds: `gateway`(cmd/gateway)。targets: `windows/amd64 v2`, `windows/arm64`,
  `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`(memguard は CGO 不要で純 Go)。
  archive: windows=zip / unix=tar.gz。checksums。changelog=git log。
- バージョン: `main.appVersion` を ldflags `-X main.appVersion={{.Version}}` で焼き、ソース既定は
  `dev`(タグビルドでのみ確定)。`gateway version`(-v)の出力はそのまま。
- CI: `.github/workflows/release.yml`(`push: tags: ['v*']` → goreleaser/action)。初回リリースは
  手動タグ `v0.3.0`。CI ジョブ `secret-scan` とは独立(リリースは goreleaser のみ)。
- README quickstart はリリース後、Release からの取得手順に更新(E2E/ローカル手順は現行のまま)。

## 6. wizard / メニュー / doctor の統合

- ウィザード [5/5] 起動方式: `1) auto-start at login + dormant (recommended)` = setup.json
  `startup:"resident"` + `autostart install`(OS 登録)+ PATH 組込提案(1 問、win のみ。
  質問数は増やさない: 既定 Yes)。`2) manual` = `startup:"manual"`+ ランチャ新形式を生成。
- `PrintSummary` 文言(PR#1 指摘の修正): startup の1行=実行される登録コマンドライン
  (`...bin\gateway.exe serve` 等)を示し、「alias」という語は消す(pi id=実モデル名)。
- メニュー 5(起動方式変更)は `ops.AutostartEnable/Disable` を呼ぶ(§7 対応表に従う)。
- doctor: item7 を autostart status の共通検査に統合(3OS 登録実体+PATH)、item6 に dormant 表示、
  旧 HKCU Run(up.cmd 参照)を発見したら「置換推奨」warn。

## 7. 検収要件(§9 第3バッチ)

1. E2E(`local-init-e2e.sh` 追加ケース):
   - init --yes --startup resident 相当で dry-run 登録定義を出力し、コマンドラインが
     `<abs>/bin/gateway(.exe) serve`(env 割当て無し)であること
   - dormant: GATEWAY_SERVE=1 未満の形で serve 起動→`/healthz` が `dormant:true`→
     認証付きリクエスト(warm)→200→`IDLE_ZERO_MINUTES` 相当経過→`/healthz` 再び `dormant:true`
     (Zero 回帰。テスト用に秒単位の上書き env: `IDLE_ZERO_MINUTES` は分のみのため、
     テスト用に `GATEWAY_DORMANT_TEST_IDLE_SECS` は導入せず、`IDLE_ZERO_MINUTES` に10進小数を許可)
2. unit: 登録定義生成器(systemd/plist/schtasks XML の文字列)+dormant 状態機械(疑似クロック)
3. 手動実機検収(この PC では任意・配布先で実施): ログオフ/ログオンで serve が立つ、pi の
   初回リクエスト(warm 含む)が通る、`up` が OS 発火で READY になる、uninstall で完全除去
4. goreleaser: `.goreleaser check` + タグ付け CI で全 target のビルド。ローカルでは `GO=… go build`
   相当のスモーク(配布 zip はリリース CI の成果物で確認)

## 8. 実装の分割と順序(このバッチ内)

| # | 内容 | 検収 |
|---|---|---|
| A | ドーマント(§3: cold/warm/zero+healthz+audit+singleflight) | unit+E2E dormant |
| B | `up` 新仕様(§1.1: probe+OS発火+案内。spawn 撤去) | unit+E2E(稼働中 probe) |
| C | autostart 3OS(§2: 登録定義+dry-run+install/uninstall/status) | unit(dry-run)+手動 |
| D | Windows `--winhide` + PATH 組込(§4) | unit+手動 |
| E | wizard[5/5]+PrintSummary+メニュー5+doctor 統合(§6) | 既存 E2E の継続緑 |
| F | goreleaser+リリース CI(§5) | `.goreleaser check`+タグ CI |

- 旧ランチャ(`up.cmd` env付き)は維持(1 リリース互換)。init の生成物は新形式(§1.2)。
- スコープ外(次バッチ): Homebrew/パッケージ配布、restart-on-fail の Windows 設定、i18n。
