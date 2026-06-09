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

# (later phases) bring up the lightweight stack
docker compose up            # default = core profile (engine + web + simulator)
```

Configuration lives in `.env` (copy from `.env.example`). The `core` profile needs none of it.

## Status

Phase 0 — Foundations. Tracking issues are labeled [`phase-0`](https://github.com/JennaLeigh311/Convergent-Routing-Analyzer/issues?q=is%3Aissue+label%3Aphase-0).
Issue conventions: see `github-issues.md`.
