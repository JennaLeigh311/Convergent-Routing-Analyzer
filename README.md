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
none of it. Convenience `make up-core` / `make up-full` wrappers arrive with the
Makefile (issue #7).

## Status

Phase 0 — Foundations. Tracking issues are labeled [`phase-0`](https://github.com/JennaLeigh311/Convergent-Routing-Analyzer/issues?q=is%3Aissue+label%3Aphase-0).
Issue conventions: see `github-issues.md`.
