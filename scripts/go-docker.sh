#!/usr/bin/env sh
# Run a Go command inside a Linux golang container. Useful on Windows, where
# the race detector needs a C toolchain and some security policies block
# fuzz-instrumented test binaries.
#
#   scripts/go-docker.sh test -race ./...
#   scripts/go-docker.sh test ./internal/extract -run='^$' -fuzz=FuzzParsePrice -fuzztime=30s
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) ROOT=$(cd "$ROOT" && pwd -W); export MSYS_NO_PATHCONV=1 ;;
esac
docker volume create pricehunter-gomod >/dev/null
exec docker run --rm \
  -v "$ROOT":/src -v pricehunter-gomod:/go/pkg/mod -w /src \
  -e CGO_ENABLED=1 -e GOFLAGS="${GOFLAGS:-}" \
  ${DOCKER_GO_ARGS:-} \
  golang:1.27 go "$@"
