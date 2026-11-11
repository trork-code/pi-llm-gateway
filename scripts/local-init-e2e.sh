#!/bin/sh
# scripts/local-init-e2e.sh — 第1バッチ配布ウィザード(gateway init)の非対話検収テスト。
#
# 検証内容(design-distribution.md §9 第1バッチ・検収要件):
#   1. 非対話 init(--yes)が runtime dir 一式(config/secrets/identity/launcher/bin/setup.json)を作る
#   2. pi models.json への gateway プロバイダーマージ(gatewayキー gk- + baseUrl + エイリアス)
#   3. 再実行で gatewayキーが流用され、ブロックが重複しない(§4.3 キー保全)
#   4. pi uninstall / pi install(再マージ)
#   5. 生成物から `gateway serve`(隠し・旧裸動作)が立ち上がり、認証が効く
#      (401未認証 → 200正キー → down)
#   6. 稼働中への変更反映(admin/reload)
#   6b. 統合(runtime dir 自己解決): ランチャを経由せず env 無しで up/status/down が完結
#      (docs/proposal-startup.md PR#1 方向: バイナリが標準配置/setup.json/identity を発見する)
#   7. セットアップ済み環境で裸 gateway(非TTY)は案内のみ(menuは対話専用)
#   8. 未セットアップ環境の隠しコマンド導線③(up → ガード文言 + exit 2)
#
# 実行: bash scripts/local-init-e2e.sh   (go と curl が必要。実ネットワークに触れない)
set -eu
cd "$(dirname "$0")/.."

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FAILS=0

log() { printf '%s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1"; FAILS=$((FAILS + 1)); }
expect() { # expect DESC WANT GOT
  if [ "$3" = "$2" ]; then log "ok   $1"; else fail "$1 (want='$2' got='$3')"; fi
}
contains() { # contains DESC SUB FILE
  if grep -q "$2" "$3" 2>/dev/null; then log "ok   $1"; else fail "$1 ($3 に '$2' が無い)"; fi
}
lacks() {
  if grep -q "$2" "$3" 2>/dev/null; then fail "$1 ($3 に '$2' があるべきでない)"; else log "ok   $1"; fi
}

# テスト用HOME(pi models.json もここに向ける)
export HOME="$WORK/home"
export USERPROFILE="$WORK/home"   # Windows(git-bash): os.UserHomeDir は USERPROFILE
mkdir -p "$HOME"
export GATEWAY_PI_MODELS="$HOME/pi/agent/models.json"
export GATEWAY_RUNTIME_DIR="$HOME/.pi-gateway"
RT="$HOME/.pi-gateway"
export OLLAMA_API_KEY="dummy-not-a-real-key"   # 上流検証は401/ネット不通非致命で続行する

log "=== ビルド ==="
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || { log "ERROR: go が見つかりません(GO=PATHを指定可)"; exit 1; }
command -v curl >/dev/null 2>&1 || { log "ERROR: curl が見つかりません"; exit 1; }
# Windowsでは生成物に .exe を付ける(up の spawn 子が os.Executable() で実行するため)。
BINOUT="$WORK/gateway"
case "$(uname)" in
  *MINGW*|*MSYS*|*CYGWIN*|*Windows*) BINOUT="$WORK/gateway.exe" ;;
esac
"$GO" build -o "$BINOUT" ./cmd/gateway

GW="$BINOUT"
# 動的ポート: 残留サーバー等との衝突を避ける(18100-18140の空きポート)
port=""
for p in $(seq 18100 18140); do
  if ! curl -s --max-time 1 -o /dev/null "http://127.0.0.1:$p/healthz" 2>/dev/null; then
    port=$p; break
  fi
done
[ -n "$port" ] || { log "ERROR: 空きポートが見つかりません(18100-18140)"; exit 1; }
log "test port: $port"
base="http://127.0.0.1:$port"

log "=== 1. 非対話 init ==="
"$GW" init --provider ollamacloud --api-key env:OLLAMA_API_KEY --model gpt-oss:120b \
  --alias pi-e2e --port "$port" --no-start --yes --print-json --quiet >"$WORK/init1.json"
expect "init --yes 成功" "0" "$?"
if [ -f "$RT/setup.json" ]; then log "ok   setup.json 生成"; else fail "setup.json が無い"; fi
if [ -f "$RT/config/models.yaml" ]; then log "ok   config 生成"; else fail "config が無い"; fi
if [ -f "$RT/secrets/identity.txt" ]; then log "ok   identity 生成"; else fail "identity が無い"; fi
if [ -f "$RT/secrets/secrets.yaml.age" ]; then log "ok   secrets 生成"; else fail "secrets が無い"; fi
if grep -q "dummy-not-a-real-key" "$RT/setup.json" "$RT/secrets"/* "$RT/gateway.log" 2>/dev/null; then
  fail "非秘匿ファイルに実キーが漏れている"
else
  log "ok   非秘匿ファイルに実キー無し"
fi
BIN="$RT/bin/gateway"
if [ "$(uname)" = "Windows_NT" ] || [ -f "$RT/bin/gateway.exe" ]; then BIN="$RT/bin/gateway.exe"; fi
if [ -f "$BIN" ]; then log "ok   bin自己コピー($BIN)"; else fail "bin自己コピー無し($BIN)"; fi
if [ -f "$RT/up.cmd" ] || [ -f "$RT/up.sh" ]; then log "ok   ランチャ生成"; else fail "ランチャ無し"; fi
contains "setup.json: provider記録" '"providers"' "$RT/setup.json"
contains "setup.json: alias記録" '"pi-e2e"' "$RT/setup.json"

log "=== 2. pi models.json マージ ==="
key1="$(grep -o 'gk-[0-9a-f]*' "$GATEWAY_PI_MODELS" | head -1)"
if [ -n "$key1" ]; then log "ok   gatewayキー(gk-)生成"; else fail "gatewayキーが無い"; fi
contains "baseUrl: ポート反映" "http://127.0.0.1:$port/v1" "$GATEWAY_PI_MODELS"
contains "pi id=実モデル名(新規約)" '"gpt-oss:120b"' "$GATEWAY_PI_MODELS"
contains "compat: system統一(Groq等の400予防)" '"supportsDeveloperRole": false' "$GATEWAY_PI_MODELS"
contains "api形式" '"openai-completions"' "$GATEWAY_PI_MODELS"

log "=== 3. 再実行(キー保全) ==="
"$GW" init --provider ollamacloud --api-key env:OLLAMA_API_KEY --model gpt-oss:120b \
  --alias pi-e2e --port "$port" --no-start --yes --print-json --quiet >"$WORK/init2.json"
key2="$(grep -o 'gk-[0-9a-f]*' "$GATEWAY_PI_MODELS" | head -1)"
expect "gatewayキー流用" "$key1" "$key2"
expect "gatewayブロック1つ" "1" "$(grep -c '"gateway": {' "$GATEWAY_PI_MODELS" || true)"
baks="$(find "$(dirname "$GATEWAY_PI_MODELS")" -maxdepth 1 -name "$(basename "$GATEWAY_PI_MODELS").bak-*" 2>/dev/null | wc -l)"
if [ "$baks" -ge 1 ]; then log "ok   バックアップ保持($baks世代)"; else fail "再実行でバックアップが無い"; fi

log "=== 4. 生成物からサーバー起動(隠し serve) ==="
AGE_IDENTITY_FILE="$RT/secrets/identity.txt" CONFIG_FILE="$RT/config/models.yaml" \
  SECRETS_FILE="$RT/secrets/secrets.yaml.age" BIND=127.0.0.1 PORT="$port" \
  "$GW" serve >"$WORK/gw.log" 2>&1 &
GWPID=$!
trap 'rm -rf "$WORK"; kill $GWPID 2>/dev/null || true' EXIT

up="no"
for _ in $(seq 1 50); do
  sleep 0.2
  c="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$base/v1/models" 2>/dev/null || true)"
  [ "$c" = "401" ] && { up="yes"; break; }
done
expect "サーバー立ち上がり(未認証401)" "yes" "$up"
c200="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$base/v1/models" -H "Authorization: Bearer $key1")"
expect "gwキーで200" "200" "$c200"
curl -s --max-time 3 "$base/v1/models" -H "Authorization: Bearer $key1" >"$WORK/models.json"
contains "エイリアス列挙(/v1/models)" '"pi-e2e"' "$WORK/models.json"

log "=== 5. pi uninstall / install(再マージ) ==="
GATEWAY_KEY="$key1" "$GW" pi uninstall >/dev/null && log "ok   pi uninstall"
lacks "uninstall後gatewayブロック無し" '"gateway": {' "$GATEWAY_PI_MODELS"
GATEWAY_KEY="$key1" "$GW" pi install >/dev/null && log "ok   pi install(再マージ)"
expect "uninstall→installでキー不変" "$key1" "$(grep -o 'gk-[0-9a-f]*' "$GATEWAY_PI_MODELS" | head -1)"

log "=== 6. 稼働中への変更反映(メニュー系コアの-notify相当) ==="
POST_OK="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/admin/reload" -H "Authorization: Bearer $key1")"
expect "admin/reload 200" "200" "$POST_OK"

kill $GWPID 2>/dev/null || true
wait $GWPID 2>/dev/null || true

log "=== 6b. 統合: ランチャを経由せず env 無しで gateway up/status/down 直接 ==="
# PR #1(docs/proposal-startup.md)の中核: バイナリが runtime dir の標準配置と
# setup.json(port/bind)/identity を自己解決する。親 up が spawn する serve も同じ発見で起動する。
CLEAR_ENV="env -u PORT -u BIND -u CONFIG_FILE -u SECRETS_FILE -u AGE_IDENTITY_FILE -u AGE_PASSPHRASE -u SECRETS_DECRYPT_CMD -u GATEWAY_ADDR -u GATEWAY_RELOAD_ADDR -u GATEWAY_KEY"
set +e
UP_OUT="$(GATEWAY_RUNTIME_DIR="$RT" $CLEAR_ENV "$GW" up 2>&1)"; up_rc=$?
set -e
printf '%s\n' "$UP_OUT" >"$WORK/up_direct.out"
expect "up 直接(env無し) rc=0" "0" "$up_rc"
contains "up直接: READY表示" "READY" "$WORK/up_direct.out"
contains "READYのポートがsetup.json由来" "http://127.0.0.1:$port/v1" "$WORK/up_direct.out"

set +e
ST_OUT="$(GATEWAY_RUNTIME_DIR="$RT" $CLEAR_ENV "$GW" status 2>&1)"; st_rc=$?
set -e
printf '%s\n' "$ST_OUT" >"$WORK/status_direct.out"
expect "status 直接(env無し) rc=0" "0" "$st_rc"
contains "status直接: 稼働ポート表示" "$port" "$WORK/status_direct.out"

set +e
DN_OUT="$(GATEWAY_RUNTIME_DIR="$RT" $CLEAR_ENV "$GW" down 2>&1)"; dn_rc=$?
set -e
printf '%s\n' "$DN_OUT" >"$WORK/down_direct.out"
expect "down 直接(env無し) rc=0" "0" "$dn_rc"
contains "down直接: 受付表示" "停止を受け付けました" "$WORK/down_direct.out"
# shutdownは応答後に300ms待って閉じる。Windowsのspawn系は実際の落ちまで数秒かかることがあるため
# 最長15s待つ(最終的に落ちなければFAIL: PR#1のspawn廃止でこの経路は消える予定)
off="no"
for _ in $(seq 1 300); do
  sleep 0.2
  c="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$base/healthz" 2>/dev/null || true)"
  if [ -z "$c" ] || [ "$c" = "000" ]; then off="yes"; break; fi
done
log "[診断] オフループ終了時の末尾応答: $c"
expect "down後 /healthz 到達不可" "yes" "$off"
if [ "$off" != "yes" ]; then
  log "[診断] healthz応答: $c / $port のLISTEN状況:"
  netstat -ano 2>/dev/null | grep ":$port" | head -5 || true
  tasklist 2>/dev/null | grep -i gateway || true
fi

log "=== 7. セットアップ済みで裸 gateway(非TTY)は案内のみ ==="
OUT="$(echo | "$GW" 2>&1)" || true
case "$OUT" in
  *"already set up"*) log "ok   案内表示(menuはTTY専用)" ;;
  *) fail "裸gatewayの済み案内が無い: $OUT" ;;
esac

log "=== 8. 未セットアップ環境の隠しコマンド導線③ ==="
unset GATEWAY_RUNTIME_DIR GATEWAY_PI_MODELS
export HOME="$WORK/home2"; export USERPROFILE="$WORK/home2"
mkdir -p "$HOME"
set +e
OUT="$(echo | "$GW" up 2>&1)"; rc=$?
set -e
case "$OUT" in
  *"not set up"*) log "ok   ガード文言表示" ;;
  *) fail "ガード文言が無い: $OUT" ;;
esac
expect "ガードは非ゼロ終了" "2" "$rc"

printf '\n'
if [ "$FAILS" -eq 0 ]; then
  log "ALL PASS — init非対話フロー検収OK"
  exit 0
fi
log "FAILED: $FAILS 件"
exit 1