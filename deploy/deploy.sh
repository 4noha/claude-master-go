#!/usr/bin/env bash
# claude-master クラウド層デプロイ（M6d）。
#
# ⚠ 対外・課金。実行前に GCP プロジェクト/課金/リージョンを確定し、
#   `gcloud auth login` 済みであること。本スクリプトは引数が無ければ
#   何もせず使い方を表示する（誤実行防止）。
#
# 使い方:
#   deploy/deploy.sh <PROJECT_ID> [REGION] [SERVICE]
#
# 行うこと:
#   1. Cloud Run に cloud-relay をソースデプロイ（min-instances=0,
#      scale-to-zero, WS 用に長め timeout, 認証は ID トークン）
#   2. Firestore セキュリティルール適用（deploy/firestore.rules）
#   3. relay URL を表示（claude-master の CLOUD_RELAY_URL に設定する）
set -euo pipefail

PROJECT="${1:-}"
REGION="${2:-asia-northeast1}"
SERVICE="${3:-claude-master-relay}"

if [[ -z "$PROJECT" ]]; then
  cat <<'USAGE'
usage: deploy/deploy.sh <PROJECT_ID> [REGION=asia-northeast1] [SERVICE=claude-master-relay]

事前確認（対外・課金）:
  - GCP プロジェクト ID / 課金有効 / リージョン
  - gcloud auth login 済み・firebase CLI（rules 適用に使用）
このスクリプトは引数指定があるまで何も実行しません。
USAGE
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> [1/2] Cloud Run: $SERVICE ($PROJECT / $REGION) をソースデプロイ"
gcloud run deploy "$SERVICE" \
  --project "$PROJECT" --region "$REGION" \
  --source "$ROOT" \
  --function "" \
  --dockerfile cloud/relay/Dockerfile \
  --min-instances 0 --max-instances 4 \
  --timeout 3600 --no-cpu-throttling \
  --allow-unauthenticated=false \
  --port 8080

echo "==> [2/2] Firestore ルール適用"
if command -v firebase >/dev/null 2>&1; then
  ( cd "$ROOT/deploy" && firebase deploy --only firestore:rules --project "$PROJECT" )
else
  echo "firebase CLI 無し。手動で deploy/firestore.rules を適用してください。" >&2
fi

URL="$(gcloud run services describe "$SERVICE" --project "$PROJECT" \
  --region "$REGION" --format='value(status.url)')"
echo "==> 完了。relay URL = $URL"
echo "    claude-master 側に CLOUD_RELAY_URL=\"${URL/https:/wss:}\" を設定。"
