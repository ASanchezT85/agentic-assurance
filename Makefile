# Phase 0 developer commands.
#
# Prerequisites (versions measured on the reference machine):
#   Go      >= 1.25   (tested on 1.26.4; pgx v5.10 sets the floor)
#   Node    >= 20     (tested on 26.3.0)
#   pnpm    >= 10     (tested on 11.8.0)
#   Python  >= 3.12   (tested on 3.14.6; numpy 2.5 stubs set the floor)
#   Docker  >= 24     (tested on 29.7.2, with the compose plugin)
#   make    >= 4      (Windows: scoop/choco, or run scripts/verify.sh instead)

# Resolved by scripts/python-bin.sh: an explicit $PY, then .venv, then PATH.
PY = $(shell sh scripts/python-bin.sh)

.PHONY: help bootstrap up down migrate test test-integration test-chaos lint typecheck build verify clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sed -e 's/:.*## /|/' -e 's/^/  /' | column -t -s '|'

bootstrap: ## Install all toolchain dependencies (creates .venv)
	sh scripts/bootstrap.sh

up: ## Start local infrastructure and wait for health
	docker compose up -d --wait
	@docker compose ps

down: ## Stop local infrastructure (keeps volumes)
	docker compose down

test: ## Run all unit tests
	go test ./...
	pnpm -r --if-present test
	$(PY) -m pytest -q

migrate: ## Apply PostgreSQL migrations (run `make up` first)
	sh scripts/migrate.sh

test-integration: ## Integration tests (run `make up && make migrate` first)
	go test -tags=integration -count=1 ./tests/integration/...

openapi: ## Regenerate docs/api/openapi.json from the routes and packages/ schemas
	go run ./cmd/openapi-gen -root .

test-load-sustained: ## Steady traffic for LOAD_MINUTES (default 2), reporting per-minute latency and codes
	GOTMPDIR=$(CURDIR)/.gotmp go test -tags=load -count=1 -v -timeout 30m ./tests/performance/ -run TestSustainedLoad

test-load-tenants: ## Several tenants under load; needs LOAD_TENANTS="tenant=token,..."
	GOTMPDIR=$(CURDIR)/.gotmp go test -tags=load -count=1 -v -timeout 20m ./tests/performance/ -run TestTenantsUnderLoad

test-live: ## Place a real order at Alpaca Paper (spec section 66 step 7). Needs ALPACA_* credentials
	go test -tags=live -count=1 -v ./tests/integration/ -run TestAlpacaPaper

test-load: ## 1,000 synthetic agents against a running gateway (spec section 56 item 1)
	GOTMPDIR=$(CURDIR)/.gotmp go test -tags=load -count=1 -v -timeout 20m ./tests/performance/ -run TestAThousandAgents

test-chaos: ## Chaos suite. STOPS REAL CONTAINERS; do not run alongside anything else
	go test -tags=chaos -count=1 -timeout 15m ./tests/chaos/...

test-race: ## Race detector, in a container (-race needs cgo; this machine has no C compiler)
	sh scripts/test-race.sh

test-race-integration: ## Race detector over the integration suite too (run `make up && make migrate` first)
	INTEGRATION=1 sh scripts/test-race.sh

lint: ## Run all linters
	@test -z "$$(gofmt -l . )" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	pnpm -C apps/console-web lint
	$(PY) -m ruff check .

typecheck: ## Run all type checkers
	pnpm -C apps/console-web typecheck
	$(PY) -m mypy

build: ## Build all deployables
	go build -o bin/ ./cmd/...
	pnpm -C apps/console-web build

verify: ## Phase 0 quality gate
	sh scripts/verify.sh

clean: ## Remove build output
	rm -rf bin apps/console-web/.next
