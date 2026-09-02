#!/usr/bin/env bash
# secrets.yaml.age の鍵ローテーション(多層防御⑨ / アーキテクチャ仕様書§9)。
#
# 使い方:
#   bash scripts/rotate-secrets.sh <recipient公開鍵>
#
# 流れ: 復号(メモリ外のtmpfs相当の一時dir) → エディタで編集 → 再暗号化 → 平文削除
# gateway本体のコード変更は不要。
set -euo pipefail

: "${AGE_IDENTITY_FILE:?AGE_IDENTITY_FILEを設定してください}"
RECIPIENT="${1:?使い方: bash scripts/rotate-secrets.sh <recipient公開鍵>}"

if [ ! -f secrets/secrets.yaml.age ]; then
  echo "secrets/secrets.yaml.age が見つかりません(リポジトリルートで実行してください)" >&2
  exit 1
fi

WORK="$(mktemp -d)"
# 平文の掃除はベストエフォート(shredがある環境では上書き削除)
cleanup() {
  if command -v shred >/dev/null 2>&1; then
    shred -u "$WORK/secrets.yaml" 2>/dev/null || true
  else
    rm -f "$WORK/secrets.yaml"
  fi
  rmdir "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

# 1. 復号(一時dirは 700、ファイルは 600 に絞る: 多層防御③)
age -d -i "$AGE_IDENTITY_FILE" -o "$WORK/secrets.yaml" secrets/secrets.yaml.age
chmod 600 "$WORK/secrets.yaml"

# 2. 編集
"${EDITOR:-vi}" "$WORK/secrets.yaml"

# 3. 再暗号化して戻す
age -r "$RECIPIENT" -o secrets/secrets.yaml.age "$WORK/secrets.yaml"
chmod 600 secrets/secrets.yaml.age

echo "ローテーション完了: secrets/secrets.yaml.age を更新しました"