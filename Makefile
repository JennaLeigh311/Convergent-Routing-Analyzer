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
#   make test         go test ./...           (no infra)
#   make lint         gofmt + go vet          (no infra)
#   make bench        run the benchmark stub on a toy graph
#   make up-core      boot the core profile (engine + web)
#   make up-full      boot the full profile (+ postgis + kafka + pipeline)
#   make integration  boot full, smoke /healthz /readyz / , tear down

# Use POSIX sh and fail fast on errors/pipes within a recipe line.
SHELL := /bin/sh

# docker compose v2 (plugin form). Override if you use the legacy binary:
#   make COMPOSE="docker-compose" up-core
COMPOSE ?= docker compose

# Endpoints the integration smoke test probes. Match docker-compose's published
# host ports (.env.example: ROUTING_SERVER_PORT=8080, WEB_PORT=3000).
ENGINE_PORT ?= 8080
WEB_PORT    ?= 3000

.DEFAULT_GOAL := help

.PHONY: help up-core up-full down clean test bench replay integration lint

## help: list the available targets
help:
	@echo "Convergent Routing Analyzer — make targets:"
	@echo "  up-core      docker compose up -d --build (core: engine + web)"
	@echo "  up-full      docker compose --profile full up -d --build (+ postgis + kafka + pipeline)"
	@echo "  down         docker compose --profile full down (stop + remove containers)"
	@echo "  clean        down -v + remove built images and Go build/test cache"
	@echo "  test         cd engine && go test ./..."
	@echo "  bench        cd engine && go run ./cmd/benchmark (toy graph; stub today)"
	@echo "  replay       cd engine && go run ./cmd/replay (stub today)"
	@echo "  lint         cd engine && gofmt check + go vet ./... (golangci-lint if present)"
	@echo "  integration  boot full, e2e smoke (engine /healthz /readyz, web /), tear down"

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

## test: run the full engine test suite (picks up fixture conformance tests)
test:
	cd engine && go test ./...

## bench: run the benchmark binary on a toy graph (scaffold stub today)
bench:
	cd engine && go run ./cmd/benchmark

## replay: run the replay binary (scaffold stub today)
replay:
	cd engine && go run ./cmd/replay

## lint: dependency-light formatting + vet; golangci-lint only if installed
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
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo ">> golangci-lint run"; \
		cd engine && golangci-lint run; \
	else \
		echo ">> golangci-lint not installed — skipping (optional)"; \
	fi

# ---- integration smoke (what lane C runs) -----------------------------------

## integration: boot the full profile, smoke the HTTP endpoints, then tear down.
## Heavy (pulls + builds images) — run lane C / CI for this, not on every change.
integration:
	@echo ">> booting full profile"
	$(COMPOSE) --profile full up -d --build
	@echo ">> waiting for engine + web to answer (up to ~60s)"
	@ok=0; \
	i=0; \
	while [ $$i -lt 30 ]; do \
		if curl -fsS "http://localhost:$(ENGINE_PORT)/healthz" >/dev/null 2>&1 \
			&& curl -fsS "http://localhost:$(ENGINE_PORT)/readyz" >/dev/null 2>&1 \
			&& curl -fsS "http://localhost:$(WEB_PORT)/" >/dev/null 2>&1; then \
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
	echo ">> tearing down full profile"; \
	$(COMPOSE) --profile full down -v --remove-orphans; \
	[ $$ok -eq 1 ]
