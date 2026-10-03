#!/usr/bin/env sh
# Exercise every API endpoint against a running server (default: local
# server mode). Prints each response; exits non-zero on an unexpected status.
#
#   scripts/smoke-api.sh                       # http://localhost:8088, dev auth
#   API=https://xyz.execute-api... TOKEN=eyJ... scripts/smoke-api.sh
set -eu
API=${API:-http://localhost:8088}
AUTH=${TOKEN:+"Authorization: Bearer $TOKEN"}
AUTH=${AUTH:-"X-Dev-User: smoke-$(date +%s)"}
URL=${PRODUCT_URL:-https://books.toscrape.com/catalogue/a-light-in-the-attic_1000/index.html}

call() { # method path expected-status [json]
  if [ -n "${4:-}" ]; then
    out=$(curl -s -w '\n%{http_code}' -X "$1" -H "$AUTH" -H 'Content-Type: application/json' -d "$4" "$API$2")
  else
    out=$(curl -s -w '\n%{http_code}' -X "$1" -H "$AUTH" "$API$2")
  fi
  code=$(printf '%s' "$out" | tail -n1)
  body=$(printf '%s' "$out" | sed '$d')
  printf '%-6s %-55s %s\n' "$1" "$2" "$code"
  [ -n "$body" ] && printf '%s\n' "$body" | head -c 600 && echo
  [ "$code" = "$3" ] || { echo "expected $3"; exit 1; }
  LAST=$body
}
# first occurrence of "name":"value" (top-level fields precede nested ones)
field() { printf '%s' "$LAST" | grep -o "\"$1\":\"[^\"]*\"" | head -n1 | cut -d'"' -f4; }

call GET /healthz 200
call GET /v1/me 200
call GET /v1/retailers 200
call POST /v1/products 201 "{\"name\":\"Smoke test book\",\"url\":\"$URL\",\"target_price\":{\"amount\":\"60.00\",\"currency\":\"GBP\"},\"check_frequency\":\"24h\"}"
ID=$(field id)
call POST /v1/products 409 "{\"name\":\"dup\",\"url\":\"$URL\",\"target_price\":{\"amount\":\"60.00\",\"currency\":\"GBP\"}}"
call POST /v1/products 422 '{"name":"","url":"http://169.254.169.254/","target_price":{"amount":"-1","currency":"USD"}}'
call GET /v1/products 200
call GET "/v1/products/$ID" 200
call PATCH "/v1/products/$ID" 200 '{"name":"Smoke test book (renamed)"}'
call GET "/v1/products/$ID/prices?max_points=200" 200
call GET "/v1/products/$ID/checks" 200
call DELETE "/v1/products/$ID" 204
call GET "/v1/products/$ID" 404
echo "smoke OK"
