.PHONY: help build test lint fmt up down load-test load-test-burst

# Go toolchain. Override if `go` is not on PATH, e.g. `make test GO=~/.local/go/bin/go`.
GO ?= go
GATEWAY_DIR := gateway
GATEWAY_BIN := bin/gateway

# k6 binary. Override if not on PATH, e.g. `make load-test K6=~/.local/bin/k6`.
K6 ?= k6
# Target for the load tests; override for a remote/compose gateway, e.g.
# `make load-test BASE_URL=http://gateway:8080`.
BASE_URL ?= http://localhost:8080
RESULTS_DIR := loadtest/results

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "} {printf "%-20s %s\n", $$1, $$2}'

build: ## Build the gateway static binary (Phase 3)
	cd $(GATEWAY_DIR) && CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o ../$(GATEWAY_BIN) ./cmd/gateway
	@echo "built $(GATEWAY_BIN) ($$(du -h $(GATEWAY_BIN) | cut -f1))"

test: ## Run gateway tests with the race detector (Phase 3)
	cd $(GATEWAY_DIR) && $(GO) test -race ./...

lint: fmt ## Lint the gateway: gofmt check, go vet, staticcheck (Phase 3)
	cd $(GATEWAY_DIR) && $(GO) vet ./...
	@command -v staticcheck >/dev/null 2>&1 && (cd $(GATEWAY_DIR) && staticcheck ./...) \
		|| echo "staticcheck not on PATH; run: $(GO) install honnef.co/go/tools/cmd/staticcheck@latest"

fmt: ## Fail if any gateway Go file is not gofmt-clean
	@unformatted=$$(cd $(GATEWAY_DIR) && gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

# Phase 4: local compose stack (gateway + 2 inference services + Prometheus +
# Grafana; see docker-compose.yml and monitoring/) and k6 load tests (see
# loadtest/k6/). `up`/`down` require Docker, which is not installed on this
# dev host — they are written and validated as YAML but have not been run
# here; their first live run is deferred to a real Docker host. The
# load-test targets need no Docker: they run k6 against natively-started
# services (gateway + uvicorn), which is how this phase's numbers were
# actually measured.
up: ## Bring up the local compose stack (requires Docker)
	docker compose up -d --build

down: ## Tear down the local compose stack and its volumes (requires Docker)
	docker compose down -v

load-test: ## Ramp load test (k6, ~3.5min) against a running gateway at BASE_URL
	@mkdir -p $(RESULTS_DIR)
	$(K6) run --env BASE_URL=$(BASE_URL) loadtest/k6/ramp.js

load-test-burst: ## Burst/rate-limit load test (k6, 30s) against a running gateway at BASE_URL
	@mkdir -p $(RESULTS_DIR)
	$(K6) run --env BASE_URL=$(BASE_URL) loadtest/k6/burst.js
