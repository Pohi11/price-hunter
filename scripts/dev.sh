#!/usr/bin/env sh
# Run the whole app locally: DynamoDB Local + ElasticMQ (docker), the demo
# store on :8081, and server mode (API + scheduler + workers + built UI) on :8088.
# Ctrl-C stops the Go processes; `docker compose down` stops the containers.
#
# For frontend hot reload, run `npm run dev` in web/ and open :5173 instead.
set -eu
cd "$(dirname "$0")/.."

docker compose up -d
[ -d web/dist ] || (cd web && npm ci && npm run build)

go run ./cmd/demostore -addr 127.0.0.1:8081 -admin-token dev &
DEMO=$!
trap 'kill $DEMO 2>/dev/null || true' EXIT INT TERM

PH_ENV=local \
PH_DYNAMODB_ENDPOINT=http://localhost:8000 \
PH_ALLOW_LOCALHOST=true \
PH_WEB_DIR=web/dist \
PH_SCHEDULER_INTERVAL=${PH_SCHEDULER_INTERVAL:-30s} \
PH_CORS_ORIGINS=http://localhost:5173 \
go run ./cmd/pricehunter serve
