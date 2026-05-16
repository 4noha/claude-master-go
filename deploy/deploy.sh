#!/usr/bin/env bash
# claude-master クラウド層デプロイ（M6）。実際に M6e で通った手順。
#
# ⚠ 対外・課金。実行前に GCP プロジェクト/課金/リージョンを確定し
#   `gcloud auth login` 済みであること。引数が無ければ何もしない。
#
# 使い方:
#   deploy/deploy.sh <PROJECT_ID> [REGION=asia-northeast1] [SERVICE=claude-master-relay]
#
# 行うこと:
#   1. API 有効化（run/firestore/cloudbuild/artifactregistry）
#   2. Firestore Native DB 作成（未作成なら）
#   3. Artifact Registry リポジトリ作成（cm）
#   4. Cloud Build で cloud/relay/Dockerfile をビルド＆push
#   5. Cloud Run デプロイ（min-instances=0 / no-cpu-throttling /
#      timeout=3600 / allow-unauthenticated。sid は claude UUID で
#      推測困難、wake 制御は Firestore 認証で別途防御）
#   6. relay URL を表示（CLOUD_RELAY_URL に wss:// で設定）
set -euo pipefail

PROJECT="${1:-}"
REGION="${2:-asia-northeast1}"
SERVICE="${3:-claude-master-relay}"
if [[ -z "$PROJECT" ]]; then
  sed -n '2,17p' "$0"; exit 2
fi
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMG="${REGION}-docker.pkg.dev/${PROJECT}/cm/${SERVICE}"

echo "==> [1/5] API 有効化"
gcloud services enable run.googleapis.com firestore.googleapis.com \
  cloudbuild.googleapis.com artifactregistry.googleapis.com --project="$PROJECT"

echo "==> [2/5] Firestore Native DB（既存ならスキップ）"
gcloud firestore databases create --location="$REGION" \
  --type=firestore-native --project="$PROJECT" 2>/dev/null || \
  echo "    (既に存在)"

echo "==> [3/5] Artifact Registry リポジトリ cm"
gcloud artifacts repositories create cm --repository-format=docker \
  --location="$REGION" --project="$PROJECT" 2>/dev/null || echo "    (既に存在)"

echo "==> [4/5] Cloud Build（cloud/relay/Dockerfile）"
( cd "$ROOT" && gcloud builds submit --project="$PROJECT" \
    --config deploy/cloudbuild.yaml --substitutions=_IMAGE="$IMG" . )

echo "==> [5/5] Cloud Run デプロイ"
gcloud run deploy "$SERVICE" --project="$PROJECT" --region="$REGION" \
  --image="$IMG" --min-instances=0 --max-instances=4 \
  --no-cpu-throttling --timeout=3600 --allow-unauthenticated --port=8080

URL="$(gcloud run services describe "$SERVICE" --project="$PROJECT" \
  --region="$REGION" --format='value(status.url)')"
echo "==> 完了。relay URL = $URL"
echo "    export CLOUD_RELAY_URL=\"${URL/https:/wss:}\""
echo "    Firestore ルール: deploy/firestore.rules（サーバ SDK は ADC/"
echo "    SA で rules 非対象。web 等を足すとき firebase deploy で適用）"
