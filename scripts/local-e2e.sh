#!/usr/bin/env bash
# scripts/local-e2e.sh — ローカル実機E2Eテスト（起動→認証→上流→ストリーミング→レート制限→停止）
#
# 実キーがなくても動く（ダミーキーで負経路と上流エラー透過を証明する）。
# OLLAMA_API_KEY を渡すと、ollamacloudへの実リクエスト(非stream/stream)まで検証する。
#
# 使い方:
#   bash scripts/local-e2e.sh                             # ダミーキー(負経路を証明)
#   OLLAMA_API_KEY=<ollama.comのAPIキー> bash scripts/local-e2e.sh
#
# 前提コマンド: go / curl / age / age-keygen
#   age は https://github.com/FiloSottile/age/releases から入手（Windowsはzip展開してPATHへ）
#
# 注意: レート制限テストを最後に実行する（成功IPも約1分ブロックされるため）。
#       日本語リクエストはUTF-8ファイル経由でcurlに渡す（Windowsコンソール経由の引数は化ける）。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/pi-gw-e2e.XXXXXX")"
PORT="${E2E_PORT:-18099}"
BASE="http://127.0.0.1:${PORT}"
GW_PID=""
PASS=0
FAIL=0

cleanup() {
  if [[ -n "$GW_PID" ]] && kill "$GW_PID" 2>/dev/null; then
    sleep 1
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf '[e2e] %s\n' "$*"; }

ok() { # ok <名前> <yes|no>
  if [[ "$2" == "yes" ]]; then
    PASS=$((PASS + 1))
    log "PASS: $1"
  else
    FAIL=$((FAIL + 1))
    log "FAIL: $1"
  fi
}

assert_eq() { # assert_eq <名前> <実際> <期待>
  if [[ "$2" == "$3" ]]; then ok "$1" yes; else ok "$1" no; log "      actual=$2 expected=$3"; fi
}

assert_contains() { # assert_contains <名前> <本文> <部分文字列>
  if printf '%s' "$2" | grep -q -- "$3"; then ok "$1" yes; else ok "$1" no; log "      bodyに '$3' なし"; fi
}

# req <curlの引数...> — 応答本文をBODY、HTTPコードをCODEにセットする
req() {
  OUT="$(curl -s -w '\n%{http_code}' --max-time 120 "$@" 2>/dev/null || true)"
  CODE="$(printf '%s' "$OUT" | tail -n 1)"
  BODY="$(printf '%s' "$OUT" | sed '$d')"
}

for bin in go curl age age-keygen; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    log "ERROR: $bin が見つかりません。PATHに追加してください"
    exit 1
  fi
done

cd "$REPO_ROOT"
log "ビルド中..."
go build -o "$WORK/gateway" ./cmd/gateway

log "鍵とsecretsを準備中... ($WORK)"
age-keygen -o "$WORK/identity.txt" >/dev/null 2>&1
chmod 600 "$WORK/identity.txt"
RECIPIENT="$(age-keygen -y "$WORK/identity.txt")"

GW_KEY="e2e-$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
cat > "$WORK/secrets.yaml" <<EOF
gateway_key: $GW_KEY
api_keys:
  ollamacloud: ${OLLAMA_API_KEY:-dummy-not-a-real-key}
EOF
age -r "$RECIPIENT" -o "$WORK/secrets.yaml.age" "$WORK/secrets.yaml"
rm -f "$WORK/secrets.yaml"
chmod 600 "$WORK/secrets.yaml.age"

cat > "$WORK/config.yaml" <<'EOF'
default_model: pi-e2e
models:
  pi-e2e:
    provider: ollamacloud
    model: gpt-oss:120b
providers:
  ollamacloud:
    base_url: https://ollama.com/v1
    api_key_env: OLLAMA_API_KEY
EOF

log "gateway起動中... (port=$PORT)"
AGE_IDENTITY_FILE="$WORK/identity.txt" CONFIG_FILE="$WORK/config.yaml" \
  SECRETS_FILE="$WORK/secrets.yaml.age" BIND=127.0.0.1 PORT="$PORT" AUTH_MAX_FAILURES=20 \
  "$WORK/gateway" >"$WORK/gateway.log" 2>&1 &
GW_PID=$!

UP="no"
for _ in $(seq 1 50); do
  sleep 0.2
  C="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$BASE/v1/models" 2>/dev/null || true)"
  if [[ "$C" == "401" ]]; then UP="yes"; break; fi
done
ok "起動(未認証アクセスは401)" "$UP"

# --- 陽性テスト(レート制限に触れない順序で実施) ---
req -H "Authorization: Bearer $GW_KEY" "$BASE/v1/models"
assert_eq "認証済みで/v1/models=200" "$CODE" "200"

req -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" \
  -d '{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}' \
  "$BASE/v1/chat/completions"
assert_eq "未知のモデルは400" "$CODE" "400"
assert_contains "エラーはOpenAI互換形(model_not_found)" "$BODY" "model_not_found"

RELOAD_OUT="$(curl -s -w '\n%{http_code}' -X POST -H "Authorization: Bearer $GW_KEY" \
  "$BASE/admin/reload" 2>/dev/null || true)"
assert_contains "admin/reload=200(reloaded)" "$RELOAD_OUT" "reloaded"

# 上流呼び出し: ダミーキーなら上流401が透過、実キーなら200と日本語応答
printf '{"model":"pi-e2e","messages":[{"role":"user","content":"日本語で「テスト成功」とだけ返してください"}],"max_tokens":800}' \
  > "$WORK/body.json"
req -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json; charset=utf-8" \
  -d "@$WORK/body.json" "$BASE/v1/chat/completions"
if [[ -n "${OLLAMA_API_KEY:-}" ]]; then
  assert_eq "実リクエスト(非stream)=200" "$CODE" "200"
  assert_contains "日本語が往復する" "$BODY" "テスト成功"
else
  assert_eq "ダミーキーの上流401が透過される" "$CODE" "401"
fi

printf '{"model":"pi-e2e","stream":true,"messages":[{"role":"user","content":"こんにちは。短く返してください。"}],"max_tokens":800}' \
  > "$WORK/body_stream.json"
if [[ -n "${OLLAMA_API_KEY:-}" ]]; then
  STREAM="$(curl -s -N --max-time 120 -H "Authorization: Bearer $GW_KEY" \
    -H "Content-Type: application/json; charset=utf-8" \
    -d "@$WORK/body_stream.json" "$BASE/v1/chat/completions" 2>/dev/null | head -c 4000 || true)"
  assert_contains "実リクエスト(stream)でSSEチャンク" "$STREAM" "chat.completion.chunk"
else
  req -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" \
    -d '{"model":"pi-e2e","stream":true,"messages":[{"role":"user","content":"hi"}]}' \
    "$BASE/v1/chat/completions"
  assert_eq "ダミーキーstreamも上流401透過" "$CODE" "401"
fi

# --- レート制限(最後: 成功IPも約1分ブロックされるため) ---
RL="no"
for i in $(seq 1 21); do
  C="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer wrong-key-$i" \
    "$BASE/v1/models" 2>/dev/null || true)"
  if [[ "$C" == "429" ]]; then RL="yes"; fi
done
ok "総当たり21回で429(レート制限)" "$RL"

log "----------------------------------------"
log "結果: PASS=$PASS FAIL=$FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  log "gateway.log:"
  head -20 "$WORK/gateway.log" || true
  exit 1
fi
log "全テスト合格 — 実機で動作することを確認しました"