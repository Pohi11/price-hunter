#!/usr/bin/env sh
# Build every Lambda as a linux/arm64 "bootstrap" binary and zip it for the
# provided.al2023 runtime. Output: dist/<function>.zip
#
#   scripts/build-lambdas.sh [version]
set -eu
cd "$(dirname "$0")/.."
VERSION=${1:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}
mkdir -p dist

for fn in api scheduler worker streams demostore; do
  src=./cmd/lambda-$fn
  [ "$fn" = demostore ] && src=./cmd/lambda-demostore
  [ -d "$src" ] || { echo "skip $fn (no $src yet)"; continue; }
  tmp=$(mktemp -d)
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath \
    -ldflags "-s -w" -o "$tmp/bootstrap" "$src"
  rm -f "dist/$fn.zip"
  if command -v zip >/dev/null 2>&1; then
    (cd "$tmp" && zip -q -X "$OLDPWD/dist/$fn.zip" bootstrap)
  else
    # Windows / Git Bash without zip: Python's zipfile is always available in CI images
    python - "$tmp/bootstrap" "dist/$fn.zip" <<'PY'
import sys, zipfile
src, dst = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(dst, "w", zipfile.ZIP_DEFLATED) as z:
    info = zipfile.ZipInfo("bootstrap", date_time=(1980, 1, 1, 0, 0, 0))
    info.external_attr = 0o755 << 16  # executable bit, required by Lambda
    info.compress_type = zipfile.ZIP_DEFLATED
    z.writestr(info, open(src, "rb").read())
PY
  fi
  rm -rf "$tmp"
  printf '%-10s %s\n' "$fn" "$(du -h "dist/$fn.zip" | cut -f1)"
done
echo "$VERSION" > dist/VERSION
echo "built version $VERSION"
