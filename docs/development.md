# Development guide

## Prerequisites

- Go (version in `go.mod`), Node 22, Docker
- Optional: `make`. Every target is a one-line command you can also run directly.

## Run everything locally

```sh
docker compose up -d          # DynamoDB Local :8000, ElasticMQ :9324 (UI :9325)
(cd web && npm ci && npm run build)
scripts/dev.sh                # demo store :8081 + API/scheduler/workers/UI on :8088
```

Open http://localhost:8088. To get 60 days of demo history, run this in another terminal while the server is running:

```sh
PH_ENV=local PH_DYNAMODB_ENDPOINT=http://localhost:8000 PH_ALLOW_LOCALHOST=true go run ./cmd/pricehunter seed
```

For UI hot reload, run `cd web && npm run dev` and open :5173. Vite proxies `/v1` to :8088.

Locally, alert emails are written to `data/outbox/*.html`, and failed-extraction snapshots go to `data/snapshots/`.

## Useful commands

| Task | Command |
|---|---|
| Extract a price from any URL | `go run ./cmd/pricehunter check https://books.toscrape.com/catalogue/a-light-in-the-attic_1000/index.html` |
| Unit tests | `go test ./...` |
| Race detector (Linux container) | `scripts/go-docker.sh test -race ./...` |
| Integration tests (needs `docker compose up`) | `go test -tags integration ./internal/store ./internal/api ./internal/e2e ./internal/queue` |
| Fuzz | `make fuzz` (on Windows: `scripts/go-docker.sh test ./internal/extract -run='^$' -fuzz=FuzzParsePrice -fuzztime=30s`) |
| Lint | `docker run --rm -v "$PWD":/src -w /src golangci/golangci-lint golangci-lint run --build-tags integration` |
| Regenerate golden extraction files | `go test ./internal/retailer -run TestGoldenPages -update` |
| Regenerate TS API types | `cd web && npm run gen:api` |
| Smoke test a running server | `go run ./cmd/smoke -api http://localhost:8088 -dev-user me -product-url http://localhost:8081/p/air-fryer` |
| Load test (demo store only) | `go run ./cmd/loadtest -dev-user load -n 1000` (start the server with `PH_MAX_PRODUCTS=5000`) |
| Build Lambda zips | `scripts/build-lambdas.sh` → `dist/*.zip` |
| Terraform checks | `terraform -chdir=infra/envs/dev init -backend=false && terraform -chdir=infra/envs/dev validate` |

## Adding a retailer

1. Save a product page as `testdata/pages/<id>/<case>.html`.
2. Add a profile to `internal/retailer/retailers.yaml` (domains, currency, strategies, and selectors if there's no structured data).
3. `go test ./internal/retailer -run TestGoldenPages -update`, then review the generated `.golden.json`.
4. Check the retailer's terms and robots.txt. Only add sites that permit low-frequency automated access.

## Windows notes

- Some security policies block freshly built test binaries in `%TEMP%`. Set `GOTMPDIR` to a folder outside `%TEMP%`, or use `scripts/go-docker.sh`.
- The race detector needs cgo; `scripts/go-docker.sh` runs it in Linux.
- Port 8080 is often taken, which is why the default is **8088** (`PH_HTTP_ADDR`).

## Configuration

All settings are environment variables, documented on the `Config` struct in `internal/config/config.go`. Invalid or unsafe combinations fail at startup. For example, `PH_ALLOW_LOCALHOST` is refused outside `PH_ENV=local`.
