# Convergent Routing Analyzer — developer Makefile (issue #7, project-spec.md §R7)
#
# Thin, real wrappers over docker compose + the Go toolchain. Recipes are
# POSIX-sh friendly (no bash-isms) so they run under the default /bin/sh.
#
# Compose profiles (from issue #6):
#   core (default)  engine + web
#   data            + postgis
#   full            + kafka + pipeline
#
# Quick reference:
#   make help         list targets
#   make test         go test -race ./...     (no infra)
#   make lint         gofmt + go vet + golangci-lint (no infra; lint required)
#   make route        run the route CLI on the toy graph (flag passthrough)
#   make bench        run the naive-router toy-graph bench
#   make up-core      boot the core profile (engine + web)
#   make up-full      boot the full profile (+ postgis + kafka + pipeline)
#   make integration  boot full, smoke /healthz /readyz / , tear down
#   make protect-main apply main's branch-protection rule (repo admin; post-CI)

# Use POSIX sh and fail fast on errors/pipes within a recipe line.
SHELL := /bin/sh

# docker compose v2 (plugin form). Override if you use the legacy binary:
#   make COMPOSE="docker-compose" up-core
COMPOSE ?= docker compose

# Note: the `integration` recipe sources published host ports from .env directly
# (ROUTING_SERVER_PORT, WEB_PORT), the same place docker compose reads them, so
# the smoke always probes the ports the stack actually publishes — no duplicated
# port literals to drift.

.DEFAULT_GOAL := help

.PHONY: help up-core up-full down clean test route bench replay integration lint protect-main

## help: list the available targets
help:
	@echo "Convergent Routing Analyzer — make targets:"
	@echo "  up-core      docker compose up -d --build (core: engine + web)"
	@echo "  up-full      docker compose --profile full up -d --build (+ postgis + kafka + pipeline)"
	@echo "  down         docker compose --profile full down (stop + remove containers)"
	@echo "  clean        down -v + remove built images and Go build/test cache"
	@echo "  test         cd engine && go test -race ./..."
	@echo "  route        cd engine && go run ./cmd/route (toy graph; flag passthrough)"
	@echo "  bench        cd engine && go run ./cmd/benchmark (naive router over the toy graph)"
	@echo "  replay       cd engine && go run ./cmd/replay (stub today)"
	@echo "  lint         cd engine && gofmt check + go vet ./... + golangci-lint (required)"
	@echo "  integration  boot full, smoke (engine /healthz /readyz, web /), tear down"
	@echo "  protect-main apply main's branch-protection rule (repo admin; post-CI)"

# ---- compose lifecycle ------------------------------------------------------

## up-core: build + start the core profile (engine + web) in the background
up-core:
	$(COMPOSE) up -d --build

## up-full: build + start the full profile (+ postgis + kafka + pipeline)
up-full:
	$(COMPOSE) --profile full up -d --build

## down: stop and remove containers across all profiles
down:
	$(COMPOSE) --profile full down

## clean: down -v (also drop named volumes) + remove built images + Go caches
clean:
	$(COMPOSE) --profile full down -v --remove-orphans
	-docker image rm cra/engine:phase0 cra/web:phase0 cra/pipeline:phase0 2>/dev/null
	cd engine && go clean -cache -testcache

# ---- Go toolchain (no infra; what lane A runs) ------------------------------

## test: run the full engine test suite with the race detector
## (-race: the engine is a concurrent HTTP server). This is also the frozen-fixture
## conformance gate: it executes BOTH the segment_id (contracts.md §1) AND the
## edge_attributes (§2) golden fixtures via their *_test.go files, so a fixture that
## drifts from its contract fails here.
test:
	cd engine && go test -race ./...

## route: run the route CLI on the toy graph; pass flags via ARGS, e.g.
## `make route ARGS="-from 40.73,-73.99 -to 40.74,-73.97"` (default flags print the
## canonical toy route). Errors go to stderr with a non-zero exit.
route:
	cd engine && go run ./cmd/route $(ARGS)

## bench: run the naive free-flow router over the toy graph and print a real
## timing summary (nodes, edges, requests routed, elapsed). Phase-1 minimal run,
## not the six-algorithm comparison harness; exits non-zero on any failure.
bench:
	cd engine && go run ./cmd/benchmark

## replay: run the replay binary (scaffold stub today)
replay:
	cd engine && go run ./cmd/replay

# Pinned golangci-lint version — keep in sync with .github/workflows/ci.yml
# (the CI gate installs this exact version) and engine/.golangci.yml's header.
# v2.12.2 is built with go1.26.2, so it parses the engine's go-1.26.4 source.
GOLANGCI_LINT_VERSION := v2.12.2

## lint: gofmt + go vet + golangci-lint (the same gate lane A runs). golangci-lint
## is REQUIRED, not optional: if it isn't installed this target fails with an
## install hint rather than silently skipping, so a degraded local gate can't
## hide a violation that CI will then reject. Pin: $(GOLANGCI_LINT_VERSION).
lint:
	@echo ">> gofmt (must report no files)"
	@cd engine && unformatted=$$(gofmt -l .); \
		if [ -n "$$unformatted" ]; then \
			echo "gofmt found unformatted files:"; echo "$$unformatted"; \
			echo "run: (cd engine && gofmt -w .)"; \
			exit 1; \
		fi; \
		echo "gofmt: clean"
	@echo ">> go vet ./..."
	cd engine && go vet ./...
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found on PATH."; \
		echo "install $(GOLANGCI_LINT_VERSION) (matches CI):"; \
		echo "  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b \$$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION)"; \
		echo "  (see https://golangci-lint.run/welcome/install/)"; \
		exit 1; \
	}
	@echo ">> golangci-lint run ($(GOLANGCI_LINT_VERSION) pinned in CI)"
	cd engine && golangci-lint run

# ---- integration smoke (what lane C runs) -----------------------------------

## integration: boot the full profile, smoke the HTTP endpoints, then tear down.
## Heavy (pulls + builds images) — run lane C / CI for this, not on every change.
## One shell with a `trap ... EXIT` so the stack is ALWAYS torn down — even if the
## `up` itself fails (e.g. a host-port collision) — and the smoke's exit code is
## preserved. Ports come from .env (the same source compose reads), defaulting to
## 8080/3000.
integration:
	@trap 'rc=$$?; echo ">> tearing down full profile"; $(COMPOSE) --profile full down -v --remove-orphans; exit $$rc' EXIT; \
	set -e; \
	echo ">> booting full profile"; \
	$(COMPOSE) --profile full up -d --build; \
	set +e; \
	set -a; [ -f .env ] && . ./.env; set +a; \
	engine_port=$${ROUTING_SERVER_PORT:-8080}; web_port=$${WEB_PORT:-3000}; \
	echo ">> waiting for engine + web to answer on :$$engine_port / :$$web_port (up to ~60s)"; \
	ok=0; i=0; \
	while [ $$i -lt 30 ]; do \
		if curl -fsS "http://localhost:$$engine_port/healthz" >/dev/null 2>&1 \
			&& curl -fsS "http://localhost:$$engine_port/readyz" >/dev/null 2>&1 \
			&& curl -fsS "http://localhost:$$web_port/" >/dev/null 2>&1; then \
			ok=1; break; \
		fi; \
		i=$$((i+1)); sleep 2; \
	done; \
	if [ $$ok -eq 1 ]; then \
		echo ">> smoke OK: engine /healthz + /readyz = 200, web / = 200"; \
	else \
		echo ">> smoke FAILED — dumping compose status + logs"; \
		$(COMPOSE) --profile full ps; \
		$(COMPOSE) --profile full logs --tail=50; \
	fi; \
	[ $$ok -eq 1 ]

# ---- branch protection (repo admin; run AFTER CI has run once on main) -------

## protect-main: apply main's branch-protection rule (idempotent; needs gh admin).
## Run after the CI workflow has reported once on main so the `CI passed` check
## exists and can be marked required. See scripts/protect-main.sh.
protect-main:
	sh scripts/protect-main.sh
