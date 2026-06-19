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
  readiness gating later) plus `/metrics` (Prometheus; Phase 0: default Go/process collectors only, real
  routing counters land in Phase 1+). Distroless image; healthcheck is a bundled Go `healthcheck` binary
  (no shell in the image).
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

## Graph connectedness: unreachability is a routing-layer concern, not a loader rejection (issue #73)

**Decision.** The `edge_attributes` loader (`engine/internal/graph/loader.go`) does **not** validate strong
(or weak) connectivity, and a disconnected component is **not** a load-time rejection. Reachability is
resolved at the **routing layer**: an unreachable origin→destination pair returns a clean "no route"
(`dijkstra` returns `found=false`; the public `Router.Route` surfaces that as a descriptive error, never a
panic, `NaN`, or divide-by-zero). One-way edges are likewise honored purely by the directed adjacency — a
router can only traverse an edge `From→To`, so a one-way corridor with no reverse row is never walked
backward.

**Why the loader does not reject disconnected graphs.** The loader's job is to validate the §2 contract
*shape* and reject a malformed artifact loudly, atomically, and fail-closed (see `loader.go`'s package
comment). Connectivity is a *topological property of valid data*, not a contract violation:

- **Real networks are legitimately disconnected.** The Porto `osm2pgrouting` extract (Phase 5) will contain
  disconnected islands, dead-end stubs, gated service roads, and ferry-only fragments. These are correct rows
  describing real geometry. Rejecting the whole artifact because one stub is unreachable would make the engine
  refuse to load real city data — exactly the silent toy↔Porto assumption this fixture exists to break.
- **Fail-closed is about contract integrity, not topology.** The loader rejects things that mean the data is
  *wrong* (bad `segment_id`, axis swap, non-finite metric, duplicate edge id). A reachable-only-from-itself
  component is *right* data; it is just not useful for a particular OD pair. That distinction belongs to the
  consumer asking the question, not to the artifact's validity.
- **The cost is paid only by the affected request.** Dijkstra already initializes every distance to `+Inf` and
  returns `found=false` when `dist[dst]` stays infinite, so an unreachable OD pair costs one failed query, not
  a corrupted graph. The equilibrium algorithms (`msa`/`systemoptimal`, Phase 3) build on this same
  shortest-path core, so handling unreachability once at the routing layer hardens all of them — they must
  treat a `found=false`/`Route` error as "this request contributes no flow", never assume every OD pair is
  reachable.
- **Decoupling.** Baking a connectivity policy into the loader would couple the data-side artifact format to a
  service-side routing assumption. The loader stays region-agnostic and topology-agnostic; the routing layer
  owns "can I get from A to B".

**What the loader still guarantees** (so the routing layer can rely on it): dense `0..NodeCount-1` node ids
and `0..EdgeCount-1` edge ids, finite-positive metric fields, `From`/`To` in range, and (issue #81) that each
edge's `length_m` is `>=` the great-circle chord between its endpoint nodes — a road is at least as long as the
straight line between its ends. That is what lets `dijkstra` use flat slices and trust that a non-`+Inf` settled
distance is a real path.

## `length_m` ≥ endpoint chord: loader guard vs. A* divisor (issue #81)

**Decision.** The `edge_attributes` loader enforces the §2 invariant `length_m >= chord(endpoints)` on every
edge (with a tiny relative tolerance, `lengthChordRelTol = 1e-9`, so a legitimately straight road where
`length_m == chord` is not rejected on float rounding). Bad geometry is rejected at the source rather than left
for each downstream consumer to defend against.

This sits on the same side of the issue #73 connectedness taxonomy as the loader's other rejections:
**wrong / contradictory data** (an axis swap, contradictory node coordinates, `length_m < chord`) is rejected
at the loader, whereas **right-but-unhelpful data** (a disconnected component) is deferred to the routing
layer. That is why `length_m < chord` rejects always-on while connectivity checking is opt-in: a length
shorter than its endpoint chord cannot be correct under any interpretation, but a disconnected graph may be
exactly the network the operator meant to load.

**A* divisor stays geometry-derived.** Even though the loader now guarantees `length_m >= chord` (so
`LengthM/FreeFlowS` would be an admissible A* heuristic divisor on conformant data), the A* router
(`engine/internal/routing/astar.go`, `maxFreeFlowSpeedMS`) **keeps** the `chord/FreeFlowS` divisor as
defense-in-depth: it is admissible *by construction* from the same endpoint geometry the heuristic measures, so
A* stays correct on any graph — a hand-built test graph, a loader bypass, or a future contract drift — without
depending on the loader invariant holding. The canonical toy fixture (`engine/testdata/toy_network.geojson`)
was regenerated for #81 by uniformly scaling its drawn geometry toward the centroid so every chord drops below
its curated `length_m`; the curated attributes are unchanged, so routing costs and the golden are unaffected.

**Future option (not built).** If an operator ever wants an *early warning* that an export is more fragmented
than expected, the right shape is an **opt-in, non-fatal** connectivity report (e.g. a `WithConnectivityWarn`
load option or a separate analysis tool that logs the component-size histogram) — a diagnostic, never a
fail-closed rejection. No trigger has fired for this yet; the routing-layer handling above is sufficient.

This decision is exercised by `engine/testdata/toy_network_adversarial.geojson` and the regression test in
`engine/internal/routing/` that asserts an unreachable OD pair returns a clean no-route and that no router
output traverses the one-way corridor against its direction.

## Deferred engineering decisions

Decisions made deliberately *not* to act on yet, recorded so the rationale and the trigger survive. Each is
tracked by a GitHub issue; the rule is to leave the simple form in place until the trigger fires, not to
pre-build for a cost we haven't measured.

**Zero-copy neighbor iteration on the router hot path (issue #35).** `AdjacencyGraph.Neighbors(n)` returns a
freshly allocated `[]Edge` and copies full `Edge` structs. That allocation is a deliberate isolation
guarantee — a caller cannot reach the internal CSR storage, which is what makes the shared graph safe for
unsynchronized concurrent reads (R5). It is the right default and stays. But on a traversal that settles
millions of nodes it will be the dominant per-route allocation source, and the CSR ranges
(`outEdgeIDs[lo:hi]`) are read-only by construction — so a router can iterate them and dereference `Edge(eid)`
itself without copying, preserving immutability *without* the per-call allocation. The zero-copy accessor is
**additive** (the safe `Neighbors` stays for non-hot callers), not a breaking change. **Trigger:** a real
router exercises the hot path (#27) *and* a benchmark confirms `Neighbors` allocation is a measured hotspot
(#31) — not before. Surfaced by the PR #34 review.

**Honest deferral of the spatial-query stubs (issue #36, decided).** The convention is: an unimplemented
spatial query **panics with an issue reference**, while `ok=false` is reserved for a genuine runtime "no
match". The `(value, ok)` contract already means "found nothing", so a stub that also returns `ok=false` is
indistinguishable from a real no-match — a half-wired integration could silently resolve *every* observation
to "no match" and never error. `NearestNode` is no longer a stub: it shipped for real in #24 (k-d tree over
node positions, built once at construction) with a brute-force real-match test, so it returns `ok=false` only
for a genuine empty/NaN case. `NearestEdge` remains unimplemented until Phase 7 (map-matching R-tree) and now
**panics** per the convention — it has no production callers today, so panicking breaks nothing and fails
loudly if someone wires map-matching prematurely. The Phase-7 map-matching issue, when filed, **must carry an
acceptance item: assert a *real* map-match (a real edge, snapped point, and distance), replacing the panic**
(and removing the panic-convention wording from the four doc sites that carry it: this file, `graph/doc.go`,
the `Graph.NearestEdge` port doc, and the `*AdjacencyGraph.NearestEdge` method/struct doc) — so `NearestEdge`
cannot ship while still effectively a stub. Surfaced by the PR #34 review.
