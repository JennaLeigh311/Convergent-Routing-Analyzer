# Where We Are — Plain-English Status & Glossary

> A companion to `plain-english-explainer.md`. That file explains **what the project is**.
> This file explains **what has actually been built so far, how the pieces fit, what's next**,
> and ends with a **glossary** of the jargon. Keep it open as a decoder ring.

---

## 1. The 30-second recap

We're building the **"brain" for a smarter GPS**. A normal navigator sends everyone down the
single fastest road and creates the traffic jam it was trying to avoid. Ours spreads drivers
across several good routes using live congestion data. We're in **Phase 0 — Foundations**:
not the brain yet, but the *skeleton, the plumbing, and the house rules* that everything else
will be built on.

---

## 2. The pieces, in plain English

Think of the system as **five services** that can run on your laptop. Right now most are
"placeholders" — real shells that start up and report healthy, but don't do the heavy work yet.

| Service | Folder | What it is | Status today |
|---|---|---|---|
| **engine** | `engine/` | The Go program that will hold the map + routing algorithms (the brain) | Skeleton: serves health checks; algorithms not wired yet |
| **web** | `web/` | The map/dashboard you'd look at in a browser | Placeholder page (real React app = much later) |
| **postgis** | (database image) | The map database (roads, intersections) | Runs; not yet loaded with a city |
| **kafka** | (message image) | The conveyor belt that carries live GPS pings | Runs; nothing flowing yet |
| **pipeline** | `pipeline/` | The job that turns raw GPS pings into "this road is X% congested" | Placeholder; real job is a later phase |

### The key files that exist now

**Plumbing / how it all boots**
- `docker-compose.yml` — the **one file that starts the whole stack**. It defines the five
  services and three "profiles" (see glossary): `core` (just engine+web, the light default),
  `data` (+database), `full` (+conveyor belt + pipeline). Everything waits for everything else
  to be **healthy** before starting, so startup isn't a race.
- `engine/Dockerfile`, `web/Dockerfile`, `pipeline/Dockerfile` — recipes for packaging each
  service into a container.
- `.env.example` — the template for configuration (ports, database name, etc.). Copy it to
  `.env` and tweak. The `core` profile needs none of it.

**The engine (Go code)** — all under `engine/`
- `cmd/routing-server/` — the actual server program. Today it only answers "are you alive?"
  (`/healthz`) and "are you ready?" (`/readyz`). The routing logic lands later.
- `cmd/healthcheck/` — a tiny helper that pings `/healthz` from inside the container.
- `cmd/benchmark/`, `cmd/replay/` — stubs (empty shells) for future tools.
- `internal/` — the building blocks, each a small focused package: `graph` (the road network),
  `routing` (the algorithms), `cost` (how "expensive" a road is), `congestion` (live traffic
  numbers), `domain` (core definitions incl. the road-ID rules), `logging` (consistent logs),
  `serveraddr` (shared address/port logic).

**The house rules (contracts)** — under `docs/`
- `docs/contracts.md` — the **three frozen agreements** that keep the five services from
  drifting apart (see glossary: *contract*, *segment_id*, *edge_attributes*, *segment-congestion*).
- `docs/fixtures/` — tiny example files both sides test against, so everyone agrees on the
  exact data shapes.
- `docs/architecture.md`, `docs/algorithms.md`, etc. — the deeper technical docs.

**Quality gates / automation**
- `Makefile` — friendly shortcuts: `make up-core` (start the light stack), `make test`,
  `make integration` (boot everything and smoke-test it), etc. Run `make help` to list them.
- `.github/workflows/ci.yml` — the **automatic checker** that runs every time code is pushed:
  formats, tests, and a web check. It must pass before anything can merge.
- `.github/workflows/integration.yml` — a heavier nightly check that boots the full stack.
- `scripts/protect-main.sh` — locks the `main` branch so all changes go through a reviewed,
  tested pull request (no sneaking changes straight in).

---

## 3. How it will all work together (the eventual flow)

1. **Map data** is downloaded (OpenStreetMap) and loaded into the **database** (`postgis`),
   which can compute road routes.
2. The database **exports the road network** as a flat file the engine reads — every road
   segment with its length, speed, and capacity (the `edge_attributes` contract).
3. Live **GPS pings** flow onto the **conveyor belt** (`kafka`).
4. The **pipeline** reads those pings, matches each to a road, and publishes "segment X is this
   congested right now" messages (the `segment-congestion` contract).
5. The **engine** holds the map + live congestion and runs its algorithms to spread drivers
   across routes — then serves results to the **web** dashboard.

The magic glue is that the engine only ever talks to the data side through **three frozen
shapes** (the contracts). Swap in a different city, dataset, or even a real Uber feed — as long
as it speaks those three shapes, the engine doesn't change.

---

## 4. What's done, and what's next

**Done so far (Phase 0):** repo skeleton & Go module · core interfaces · structured logging ·
the three frozen contracts (road IDs, road export, congestion messages) · the docker-compose
stack with health checks · the Makefile + automated CI · `main` branch protection.

**Immediately next:** **Issue #15** — add an automatic test that checks the road-network export
example stays internally consistent (so a typo in the data fails the build instead of silently
producing a wrong map). It's a small database-side safety net.

**Bigger phases ahead (later):** load a real city into the database → build the routing
algorithms in the engine → build the GPS pipeline for real → build the React map → run the
benchmark that produces the headline "X% less total travel time" number.

---

## 5. Glossary — the jargon, decoded

**Project-specific terms**
- **segment_id** — the **one agreed name for a piece of road**, used everywhere (database,
  pipeline, engine). Built from the road's permanent OpenStreetMap ID so it never shifts. Format
  `"{osm_way_id}:{seq}:{dir}"`. If two services disagreed on this, congestion would silently land
  on the wrong road — hence it's frozen.
- **wire key** — just another name for `segment_id`: the identifier that travels "across the
  wire" between services. (The doc warns *never* use a database row number as the wire key,
  because row numbers change every time you reload the data.)
- **edge_attributes** — the **exported description of every road**: its ID, endpoints, length,
  free-flow time, and capacity. The engine and web read it. A frozen contract.
- **segment-congestion** — the **live "this road is busy" message** the pipeline sends to the
  engine. A frozen contract.
- **contract / frozen / freeze** — a written-down agreement on a data shape that nobody may
  change casually (others depend on it). Changing one means bumping a version number and telling
  everyone. The "freeze" prevents silent, no-error-but-wrong-answer failures.
- **fixture / golden fixture** — a small example file used as the source of truth in tests, so
  both sides prove they produce/read the exact same shape.
- **convergent routing problem** — the core problem: GPS sending everyone down the same fastest
  road *creates* the jam. Our project distributes them instead.

**Map / database terms**
- **PostGIS** — an add-on to the Postgres database that understands maps and geography.
- **pgRouting** — a further add-on that computes routes (shortest paths) *inside* the database.
- **OpenStreetMap (OSM)** — the free, crowd-sourced world map we get road data from.
- **osm_way_id / osm2pgrouting** — `osm_way_id` is OSM's permanent ID for a road; `osm2pgrouting`
  is the tool that imports OSM data into the routing database.
- **capacity** — how many cars per hour a road handles before it clogs.
- **free-flow time/speed** — how long a road takes with zero traffic.
- **BPR** — a standard formula that turns "how full is this road" into "how slow is it now."

**Infrastructure terms**
- **container / image / Docker** — a *container* is a service packaged with everything it needs
  so it runs the same anywhere; an *image* is the packaged recipe; *Docker* runs them.
- **docker-compose** — the tool that starts several containers together from one file.
- **profile (core/data/full)** — a switch that picks how much of the stack to start: `core`
  (light: engine+web), `data` (+database), `full` (everything).
- **healthcheck / `/healthz` / `/readyz`** — automatic "are you OK?" pings. `/healthz` = "the
  process is alive"; `/readyz` = "ready to actually serve."
- **Kafka** — the message "conveyor belt" that reliably carries streams of events (GPS pings).
- **KRaft** — Kafka's modern mode that runs without a separate helper service (simpler setup).
- **topic / broker** — a *topic* is a named channel on the belt (e.g. `gps-pings`); a *broker*
  is the Kafka server itself.
- **Spark / PySpark / `local[*]` / Structured Streaming** — Spark is a data-processing engine;
  PySpark is its Python interface; `local[*]` means "run it as a library on this one machine,
  not a cluster"; Structured Streaming = processing data continuously as it arrives.
- **watermark / event-time window** — bookkeeping that handles GPS pings arriving late or out of
  order, so congestion counts stay correct and aren't double-counted.
- **distroless** — a stripped-down container image with no shell or extra tools (smaller, safer).
- **PG_DSN** — the connection string that tells a service how to reach the Postgres database.

**Code / process terms**
- **Go / Goroutine** — Go is the language the engine is written in; a *goroutine* is its
  lightweight way of doing many things at once (e.g. handling requests in parallel).
- **ports-and-adapters** — a design style where the core logic talks to the outside world only
  through clean interfaces (so Kafka/Spark/Postgres can be swapped without touching the brain).
- **slog / structured logging** — logs written as consistent key-value data (queryable) instead
  of free-form text.
- **CI / CI lane / `CI passed`** — *CI* (Continuous Integration) is the automatic checker that
  runs on every change. A *lane* is one group of checks (Lane A = Go, Lane B = web). `CI passed`
  is the single gate that must be green to merge.
- **branch protection / pull request (PR)** — `main` is locked: you can't push to it directly;
  you open a *pull request* (a proposed change) that must pass CI before it can be merged.
- **race detector (`-race`)** — a tool that catches bugs where parallel tasks step on each other.
- **squash-merge** — combining all of a change's commits into one tidy commit when merging.

---

*Last updated: 2026-06-10, after Phase-0 issue #7 (Makefile + CI + branch protection) shipped.*
