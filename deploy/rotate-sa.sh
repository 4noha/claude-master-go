#!/usr/bin/env bash
# cm-agent SA 鍵ローテーション（個人運用・手動）。
#
# ⚠ 不可逆・対外。旧鍵を削除するので、その鍵で enroll 済みの **他 PC は
#   切断**される（再 enroll/更新が必要）。実行前に把握すること。
#
# 行うこと:
#   1. 新しい SA 鍵を発行 → ~/.claude-master/sa.json を置換（600）
#   2. ローカル launchd（monitor / cloud agent）を新鍵で再起動
#   3. Cloud Run の ENROLL_SA_JSON_B64 を新鍵へ更新（enroll 配布物を更新）
#   4. 旧ユーザー管理鍵を全削除（新鍵のみ残す）
#
# 使い方: deploy/rotate-sa.sh <PROJECT_ID> [SA_EMAIL] [SERVICE] [REGION]
set -euo pipefail
PROJECT="${1:-}"
SA="${2:-cm-agent@${PROJECT}.iam.gserviceaccount.com}"
SERVICE="${3:-claude-master-relay}"
REGION="${4:-asia-northeast1}"
[[ -z "$PROJECT" ]] && { sed -n '2,18p' "$0"; exit 2; }

SAF="$HOME/.claude-master/sa.json"
OLD="$(python3 -c "import json;print(json.load(open('$SAF'))['private_key_id'])" 2>/dev/null || echo '')"
echo "==> [1/4] 新 SA 鍵発行（$SA）"
TMP="$(mktemp /tmp/cmsa.XXXX.json)"
gcloud iam service-accounts keys create "$TMP" --iam-account="$SA" --project="$PROJECT"
NEW="$(python3 -c "import json;print(json.load(open('$TMP'))['private_key_id'])")"
cp "$TMP" "$SAF"; chmod 600 "$SAF"; rm -f "$TMP"
echo "    新 key_id=$NEW  旧 key_id=${OLD:-?}"

echo "==> [2/4] ローカル launchd を新鍵で再起動"
U="$(id -u)"
launchctl kickstart -k "gui/$U/com.4noha.claude-master" 2>/dev/null || true
launchctl kickstart -k "gui/$U/com.4noha.claude-master-cloud" 2>/dev/null || true

echo "==> [3/4] Cloud Run ENROLL_SA_JSON_B64 を新鍵へ更新"
B64="$(base64 < "$SAF" | tr -d '\n')"
gcloud run services update "$SERVICE" --project="$PROJECT" --region="$REGION" \
  --update-env-vars="ENROLL_SA_JSON_B64=${B64}" >/dev/null
echo "    更新済（新リビジョン配信）"

echo "==> [4/4] 旧ユーザー管理鍵を削除（新鍵のみ残す）"
for k in $(gcloud iam service-accounts keys list --iam-account="$SA" \
    --project="$PROJECT" --managed-by=user --format='value(name.basename())'); do
  if [[ "$k" != "$NEW" ]]; then
    gcloud iam service-accounts keys delete "$k" --iam-account="$SA" \
      --project="$PROJECT" --quiet && echo "    削除 $k"
  fi
done
echo "==> 完了。新 key_id=$NEW のみ有効。"
echo "    他 PC は旧鍵失効で切断＝当該 PC で再 enroll/更新が必要。"
