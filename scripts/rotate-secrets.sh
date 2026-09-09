#!/usr/bin/env bash
# secrets.yaml.age の鍵ローテーション(多層防御⑨ / アーキテクチャ仕様書§9)。
#
# 使い方:
#   bash scripts/rotate-secrets.sh [--skip-verify] <recipient>...
#
#   <recipient>...  再暗号化先の公開鍵。複数指定可(多層防御②:
#                   誰の鍵でも復号できる状態ではなく「決まった環境の鍵でしか
#                   復号できない」ように絞る)
#   --skip-verify   identity(秘密鍵)自体をローテーションする場合のみ使用。
#                   現在のidentityでは新しいファイルを復号できないため検証を省略する
#
# 流れ: 復号 → 編集 → 再暗号化 → 復号ラウンドトリップ検証 → 置き換え(旧ファイルは.bak退避)
set -euo pipefail

: "${AGE_IDENTITY_FILE:?AGE_IDENTITY_FILEを設定してください}"

SKIP_VERIFY=0
RECIPIENTS=()
for arg in "$@"; do
  case "$arg" in
    --skip-verify) SKIP_VERIFY=1 ;;
    -*) echo "不明なオプション: $arg" >&2; exit 1 ;;
    *) RECIPIENTS+=("$arg") ;;
  esac
done
if [ "${#RECIPIENTS[@]}" -eq 0 ]; then
  echo "使い方: bash scripts/rotate-secrets.sh [--skip-verify] <recipient>..." >&2
  exit 1
fi
if [ ! -f secrets/secrets.yaml.age ]; then
  echo "secrets/secrets.yaml.age が見つかりません(リポジトリルートで実行してください)" >&2
  exit 1
fi

WORK="$(mktemp -d)"
cleanup() {
  if command -v shred >/dev/null 2>&1; then
    shred -u "$WORK/secrets.yaml" "$WORK/candidate.age" 2>/dev/null || true
  else
    rm -f "$WORK/secrets.yaml" "$WORK/candidate.age"
  fi
  rmdir "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

# 1. 復号(一時dirは 700、ファイルは 600 に絞る: 多層防御③)
age -d -i "$AGE_IDENTITY_FILE" -o "$WORK/secrets.yaml" secrets/secrets.yaml.age
chmod 600 "$WORK/secrets.yaml"

# 2. 編集
"${EDITOR:-vi}" "$WORK/secrets.yaml"

# 3. 再暗号化(複数recipient可)
AGE_ARGS=()
for r in "${RECIPIENTS[@]}"; do
  AGE_ARGS+=(-r "$r")
done
age "${AGE_ARGS[@]}" -o "$WORK/candidate.age" "$WORK/secrets.yaml"
chmod 600 "$WORK/candidate.age"

# 4. 検証: 本物の鍵で復号できることを確認してから置き換える(誤暗号化の流出を防ぐ)
if [ "$SKIP_VERIFY" -eq 1 ]; then
  echo "警告: 復号ラウンドトリップ検証をスキップしました。" >&2
  echo "      新しいidentityで手動復号してからgatewayを再起動してください" >&2
else
  if ! age -d -i "$AGE_IDENTITY_FILE" "$WORK/candidate.age" | cmp -s - "$WORK/secrets.yaml"; then
    echo "検証失敗: 新しいsecrets.yaml.ageを現在のidentityで復号できません。" >&2
    echo "identityもローテーションする場合は --skip-verify を付け、" >&2
    echo "新しいidentityでの復号確認を先に行ってください" >&2
    exit 1
  fi
fi

# 5. 置き換え(旧ファイルは.bakに退避: 誤操作・鍵紛失時の復旧用。暗号化済みなので残置は安全)
if [ -f secrets/secrets.yaml.age ]; then
  cp -p secrets/secrets.yaml.age secrets/secrets.yaml.age.bak
  chmod 600 secrets/secrets.yaml.age.bak
fi
mv "$WORK/candidate.age" secrets/secrets.yaml.age
chmod 600 secrets/secrets.yaml.age

echo "ローテーション完了: secrets/secrets.yaml.age を更新しました(旧: secrets.yaml.age.bak)"