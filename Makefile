# Price Hunter developer tasks. On Windows without make, run the commands
# shown in each recipe directly (see docs/development.md).

GO        ?= go
PKGS      := ./...
FUZZTIME  ?= 30s

.PHONY: help build test test-race test-integration fuzz lint fmt demostore check dev web web-test api-types clean

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

build: ## build local binaries into bin/
	$(GO) build -o bin/pricehunter ./cmd/pricehunter
	$(GO) build -o bin/demostore ./cmd/demostore

test: ## unit tests
	$(GO) test $(PKGS)

test-race: ## unit tests with the race detector (needs cgo)
	$(GO) test -race -count=1 $(PKGS)

fuzz: ## run each fuzz target for FUZZTIME
	$(GO) test ./internal/domain  -run='^$$' -fuzz=FuzzParseAmount -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/extract -run='^$$' -fuzz=FuzzParsePrice  -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/urlx    -run='^$$' -fuzz=FuzzValidate    -fuzztime=$(FUZZTIME)

test-integration: ## integration tests (needs: docker compose up -d)
	$(GO) test -tags integration -count=1 ./internal/store ./internal/api ./internal/e2e

lint: ## golangci-lint
	golangci-lint run

fmt: ## gofmt + goimports
	gofmt -w cmd internal

demostore: ## run the demo store on :8081
	$(GO) run ./cmd/demostore -addr :8081 -admin-token dev

check: ## extract a price: make check URL=https://...
	$(GO) run ./cmd/pricehunter check --allow-localhost "$(URL)"

dev: ## run everything locally (docker + demo store + server mode + UI) on :8088
	scripts/dev.sh

web: ## build the web UI into web/dist
	cd web && npm ci && npm run build

web-test: ## typecheck + unit-test the web UI
	cd web && npm run typecheck && npm test

api-types: ## regenerate TypeScript API types from api/openapi.yaml
	cd web && npm run gen:api

golden: ## regenerate golden extraction files after an intended change
	$(GO) test ./internal/retailer -run TestGoldenPages -update

clean:
	rm -rf bin dist coverage.out
