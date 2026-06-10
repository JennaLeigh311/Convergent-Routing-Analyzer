# Convergent Routing Analyzer

A demand-aware traffic routing engine that tackles the **convergent routing problem**: when every GPS
navigator sends drivers down the same shortest path, it manufactures the very congestion it was avoiding.
This service distributes simultaneous navigation requests across multiple paths using live congestion
data, and benchmarks several traffic-assignment algorithms (naive shortest-path, reactive, incremental,
user-equilibrium via MSA, system-optimal, and multi-path split) to show which best minimizes total
network travel time.

Built with **Go** (routing engine), **Apache Kafka** + **Apache Spark** (GPS ingestion & congestion
aggregation), and **React** (real-time visualization).

## Architecture

Ports-and-adapters: the Go engine's domain (graph + algorithms) depends only on the `Graph`,
`CongestionProvider`, `CostFunction`, and `Router` interfaces — it knows nothing about Kafka, Spark, or
PostGIS. The entire routing brain and benchmark run against a simulated congestion source before any
broker exists. See `docs/architecture.md` and the frozen cross-team contracts in `docs/contracts.md`.

## Repository layout

```
engine/        Go routing service
  cmd/         routing-server, benchmark, replay binaries
  internal/    graph, congestion, cost, routing, benchmark, api packages
pipeline/      Spark Structured Streaming jobs (map-match + windowed aggregation)
data/scripts/  dataset download + osm2pgrouting + graph-export tooling
web/           React + deck.gl/Leaflet frontend
docs/          architecture, algorithms, data-pipeline, api, benchmarks, contracts
```

## Quickstart

```bash
# Build & test the engine
cd engine && go build ./... && go test ./...
```

## Running the stack

The stack boots through three additive docker-compose profiles — `core` is the
default and the laptop-light option; `data`/`full` are opt-in. See
[`docs/architecture.md`](docs/architecture.md) for per-profile services,
resource footprints, and startup order.

```bash
docker compose up                  # core (default): engine + web
docker compose --profile data up   # + PostGIS / pgRouting
docker compose --profile full up   # + Kafka (KRaft) + Spark pipeline (local[*])
```

Configuration lives in `.env` (copy from `.env.example`); the `core` profile needs
none of it. The `make up-core` / `make up-full` wrappers (below) build + start the
stack detached.

### Make targets

A root `Makefile` wraps the common workflows (run `make help` for the list):

| Target         | What it does                                                              |
| -------------- | ------------------------------------------------------------------------ |
| `up-core`      | `docker compose up -d --build` — core profile (engine + web)             |
| `up-full`      | `docker compose --profile full up -d --build` — + postgis + kafka + pipeline |
| `down`         | `docker compose --profile full down` — stop + remove containers          |
| `clean`        | `down -v` + remove built images and the Go build/test cache              |
| `test`         | `cd engine && go test ./...`                                             |
| `bench`        | `cd engine && go run ./cmd/benchmark` (toy graph; scaffold stub today)   |
| `replay`       | `cd engine && go run ./cmd/replay` (scaffold stub today)                 |
| `lint`         | `gofmt` check + `go vet ./...` (runs `golangci-lint` only if installed)  |
| `integration`  | boot `full`, smoke engine `/healthz` + `/readyz` and web `/`, tear down  |

### CI

GitHub Actions runs three lanes (`.github/workflows/`):

- **Lane A** (`ci-go.yml`) — push + PR: `gofmt`, `go vet`, `go test ./...`, and the
  toy-graph benchmark. Go pinned to 1.25. No Docker.
- **Lane B** (`ci-web.yml`) — push + PR: validates the `web/` static placeholder
  (`index.html` well-formedness + `Dockerfile` sanity). Expands to a real
  `node build` + lint when the React app lands in Phase 10.
- **Lane C** (`integration.yml`) — nightly cron + manual `workflow_dispatch` (NOT on
  push): boots the `full` profile and runs the same e2e smoke as `make integration`.

`main` is protected: PRs must pass **Lane A** and **Lane B** (required status checks,
branches up to date) before merge; direct pushes to `main` are blocked. Lane C is
informational/gated, not a merge gate.

## Status

Phase 0 — Foundations. Tracking issues are labeled [`phase-0`](https://github.com/JennaLeigh311/Convergent-Routing-Analyzer/issues?q=is%3Aissue+label%3Aphase-0).
Issue conventions: see `github-issues.md`.
