# Architecture

> Stub. Expanded from `project-spec.md §2`. Covers the ports-and-adapters design, the data/service/
> presentation planes, the four core ports, and per-profile resource footprints (issue #6).

## Conventions (set in Phase 0)

**Go module rooted at `engine/`, not the repo root.** This is a polyglot repo (Go engine, PySpark
pipeline, React frontend). Rooting the module at `engine/` keeps `go build ./...` from scanning
`pipeline/`, `web/`, and `data/`, and gives each language ecosystem its own root. A future second Go module
or a `go.work` workspace can be added without disturbing this one. Do not "fix" the module to the repo root.

**Shared identifiers live in `internal/domain`.** `EdgeID`, `NodeID`, `SegmentID`, `LatLon`, and
`Direction` live in the `domain` leaf package, which imports nothing from the rest of the engine and may be
imported by all. This keeps `congestion`, `cost`, `api`, etc. from depending on `graph` just to name an ID,
and keeps the dependency direction acyclic.

**Ports at package root, adapters in subpackages.** Each port interface (`Graph`, `CongestionProvider`,
`CostFunction`, `Router`) is defined at its package root. Concrete adapters go in subpackages
(`congestion/memory`, `congestion/static`, `congestion/kafka`, `graph/loader`, …). Import paths — not
reviewer vigilance — keep the engine core free of transport dependencies: the core imports the port
package, never an adapter subpackage that pulls in a Kafka client or PostGIS driver.

**Structured logging via `log/slog`, configured once per binary.** All logging goes through
`internal/logging`, a thin wrapper over the stdlib `log/slog`. Each binary calls `logging.Setup()` at
process start, which builds a logger from `LOG_LEVEL` (`debug|info|warn|error`) and `LOG_FORMAT`
(`text|json`) and installs it as the slog default. Use `text` in development and `json` in deployed
environments for aggregation. Prefer structured key/value fields over formatted strings
(`logger.Info("routed", "requests", n, "algo", name)`, not `fmt.Sprintf`) so logs stay queryable; never
use bare `fmt.Println` for diagnostics. Entrypoints (`cmd/*`) hold the explicit `*slog.Logger` returned by
`Setup`; library and leaf packages take an injected `*slog.Logger` or use `slog.Default()`, never
constructing their own. **Never log secrets, credentials, full DSNs, or API keys, and treat raw GPS
coordinates as user-location PII** — log only derived values (segment IDs, counts), never raw lat/lon tied
to a device.

## Compose profiles & runtime footprint (issue #6, §R7)

The stack boots through three **additive** docker-compose profiles. `core` is the default — the "one
command" (`docker compose up`) story targets it; the heavy infrastructure is strictly opt-in so a laptop
isn't asked to run a 4-heavy-service stack by default. Profiles are additive: `core` services carry no
`profiles:` key (always on), `data` adds PostGIS, `full` adds Kafka + the Spark pipeline.

| Profile | Command | Services | Adds |
|---|---|---|---|
| `core` (default) | `docker compose up` | `engine`, `web` | — |
| `data` | `docker compose --profile data up` | core + `postgis` | PostGIS + pgRouting |
| `full` | `docker compose --profile full up` | data + `kafka`, `pipeline` | Kafka (KRaft) + Spark (`local[*]`) |

**Service notes**

- **engine** — the routing-server. Hosts the simulator congestion adapter **in-process**; there is *no*
  separate simulator container. Exposes `/healthz` + `/readyz` (Phase 0: both 200 `ok`; `/readyz` gains real
  readiness gating later). Distroless image; healthcheck is a bundled Go `healthcheck` binary (no shell in
  the image).
- **web** — Phase-0 placeholder (`nginx:1.27-alpine` serving a static page). The React app lands in Phase 10.
- **postgis** — `pgrouting/pgrouting:16-3.5-3.7.3` (Postgres 16 / PostGIS 3.5 / pgRouting 3.7.3). Healthcheck:
  `pg_isready`.
- **kafka** — `apache/kafka:3.7.1`, **single-node KRaft, no Zookeeper**. Healthcheck:
  `kafka-broker-api-versions` against the PLAINTEXT listener.
- **pipeline** — PySpark **`local[*]`** (Spark as a library, not a master/worker cluster). Phase-0 placeholder
  (`python:3.11-slim` + `pyspark==3.5.1`); the real Structured Streaming job lands in Phase 7.

**Startup order (healthcheck gating).** Dependents wait on `depends_on: { condition: service_healthy }`, so
startup is deterministic rather than racy (the #1 demo-failure mode per §R7):

- `core`: `engine` and `web` start in parallel; nothing depends on anything.
- `data`: `postgis` becomes healthy via `pg_isready` before consumers connect.
- `full`: `kafka` must be healthy (broker-API check) and `postgis` healthy **before** `pipeline` starts.

**Resource footprint per profile (rough, dev laptop).**

| Profile | Containers | Approx. RAM | Notes |
|---|---|---|---|
| `core` | 2 | ~50–100 MB | engine (static Go, single-digit MB) + nginx. Light; the laptop default. |
| `data` | 3 | ~+300–500 MB | adds PostGIS; Postgres baseline + shared buffers. |
| `full` | 5 | ~+2–4 GB | adds JVM Kafka (~1 GB) + a JVM Spark `local[*]` driver (~1–2 GB heap). Heavy — the explicit scale path, not the default. |

Config is env-driven (`.env`, copied from `.env.example`); `core` needs none of it. No service hardcodes a
broker address, PG DSN, or topic name — they interpolate from the environment so the dev↔full adapter swap is
config-driven.
