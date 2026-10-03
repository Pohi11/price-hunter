#!/usr/bin/env sh
# Build once, upload immutable artifacts, apply Terraform, publish the UI.
# Used by the deploy workflow; also runnable by hand with AWS credentials.
#
#   scripts/deploy.sh dev  [version]
#   scripts/deploy.sh prod <version>     # promote an already-built version
#
# Requires: infra/envs/<env>/backend.hcl and either terraform.tfvars or
# TF_VAR_artifacts_bucket in the environment.
set -eu
ENV=${1:?usage: deploy.sh <dev|prod> [version]}
VERSION=${2:-$(git rev-parse --short=12 HEAD)}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TF="terraform -chdir=$ROOT/infra/envs/$ENV"

BUCKET=${TF_VAR_artifacts_bucket:-$(sed -n 's/^artifacts_bucket *= *"\(.*\)"/\1/p' "$ROOT/infra/envs/$ENV/terraform.tfvars" 2>/dev/null)}
[ -n "$BUCKET" ] || { echo "artifacts bucket unknown: set TF_VAR_artifacts_bucket"; exit 1; }

# 1. Artifacts. Prod never builds: it deploys the exact bytes tested in dev,
#    either already in its bucket or handed over as dist/ (CI passes the
#    dev job's zips to the prod job, which may be a separate AWS account).
if aws s3 ls "s3://$BUCKET/$VERSION/api.zip" >/dev/null 2>&1; then
  echo "artifacts for $VERSION already in s3://$BUCKET; reusing them"
else
  if [ "$(cat "$ROOT/dist/VERSION" 2>/dev/null)" != "$VERSION" ]; then
    [ "$ENV" = prod ] && { echo "refusing to build for prod: no tested artifacts for $VERSION"; exit 1; }
    "$ROOT/scripts/build-lambdas.sh" "$VERSION"
  fi
  for z in "$ROOT"/dist/*.zip; do
    aws s3 cp "$z" "s3://$BUCKET/$VERSION/$(basename "$z")" --only-show-errors
  done
fi

# 2. Infrastructure + code.
$TF init -input=false -backend-config=backend.hcl >/dev/null
$TF apply -input=false -auto-approve -var "app_version=$VERSION"

# 3. Frontend: same build for every environment; config.json comes from Terraform.
WEB_BUCKET=$($TF output -raw -json app | python -c 'import json,sys; print(json.load(sys.stdin)["web_bucket"])')
DIST_ID=$($TF output -raw -json app | python -c 'import json,sys; print(json.load(sys.stdin)["cloudfront_distribution_id"])')
(cd "$ROOT/web" && npm ci --silent && npm run build --silent)
aws s3 sync "$ROOT/web/dist" "s3://$WEB_BUCKET" --delete --exclude config.json \
  --cache-control "public,max-age=31536000,immutable" --exclude index.html --only-show-errors
aws s3 cp "$ROOT/web/dist/index.html" "s3://$WEB_BUCKET/index.html" --cache-control "no-cache" --only-show-errors
aws cloudfront create-invalidation --distribution-id "$DIST_ID" --paths /index.html /config.json >/dev/null

$TF output -json app | python -c 'import json,sys; o=json.load(sys.stdin); print("web:", o["web_url"]); print("api:", o["api_url"])'
echo "deployed $VERSION to $ENV"
