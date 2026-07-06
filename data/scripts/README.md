# Data scripts — Porto `edge_attributes` pipeline

Turns a small, bounded **real Porto** OpenStreetMap extract into a
contract-conformant `edge_attributes.geojson` (`docs/contracts.md` §1 `segment_id`
and §2 `edge_attributes`) that the Go engine loads **unchanged** through
`engine/internal/graph.LoadEdgeAttributesGeoJSON`.

> Phase 8, issue #120. (This README previously claimed "implemented in Phase 5" —
> that was a scaffold placeholder; the pipeline is real as of Phase 8.)

## Quick start

```sh
make data-porto          # fetch → load → export, end to end
```

Produces `data/out/edge_attributes.geojson` (a few thousand directed edges for
central Porto). The OSM extract (`data/osm/porto.osm`) and the export
(`data/out/edge_attributes.geojson`) are **git-ignored data blobs** — the scripts
here are the committed, reproducible producers; the blobs are never committed.

If host port 5432 is already taken (a local Postgres), publish the compose
Postgres elsewhere — the pipeline reaches it over the compose network, so the
host port is irrelevant to correctness:

```sh
PG_PORT=55432 make data-porto
```

## What runs

`make data-porto` calls `run_pipeline.sh`, which orchestrates five steps on top of
the existing `data` compose profile (PostGIS 16 / PostGIS 3.5 / pgRouting 3.7.3):

| # | Step | Script | Runs in |
|---|------|--------|---------|
| 1 | Fetch a bounded Porto OSM extract (7 vehicle highway classes + `_link`) | `fetch_porto_osm.sh` | host (`curl` → Overpass) |
| 2 | Bring up `postgis`, wait healthy, `CREATE EXTENSION postgis/pgrouting` | `run_pipeline.sh` | `docker compose --profile data` |
| 3 | Build the `cra/data-tools:phase8` client image (osm2pgrouting + python exporter) | `Dockerfile` | `docker build` |
| 4 | `osm2pgrouting`-load the extract → `ways` split at intersections | `run_pipeline.sh` | `cra/data-tools` on the compose network |
| 5 | Export `edge_attributes.geojson` from pgRouting topology + raw OSM tags | `export_edge_attributes.py` | `cra/data-tools` on the compose network |

The `cra/data-tools` client joins the **same docker network** as the `postgis`
service and reaches it by service name `postgis:5432`, so there is no
host-port/loopback dependency.

## How the contract is met

- **`segment_id = "{osm_way_id}:{seq}:{dir}"`** (§1). `osm_way_id` is
  `ways.osm_id` (the OSM way id — never the auto-increment `gid`, which §1 bans as
  a wire key). `seq` is the 0-based ordinal of the sub-segment among its way's
  **emitted** sub-segments after pgRouting splits it at intersections (`gid`
  order; a skipped degenerate/self-loop sub-segment does not leave a gap). `dir` is `F`
  (along the way's node ordering) or `R` (against it). A **two-way** way emits
  both `F` and `R`; a **one-way** way emits only its permitted direction (from the
  `oneway`/`junction` tags and OSM defaults — motorways and roundabouts are
  one-way by convention).

- **Division of labour.** pgRouting provides *topology only*: `osm_id`, the
  source/target vertex ids, the split geometry, and vertex coordinates. Every §2
  derived field (`highway_class`, `lanes_effective`, `maxspeed_kmh`,
  `class_factor`, `capacity_vph`, `freeflow_time_s`) is derived from the **raw OSM
  tags** exactly per the §2 tables — the exporter does **not** trust
  osm2pgrouting's own speed/lane defaulting, which uses a different default table.
  `lanes_effective` follows the full §2 precedence (direction-specific tag →
  bare-lanes-if-one-way → `floor(bare/2)` for two-way → class default).

- **`length_m >= endpoint chord` (§2 invariant).** `length_m` is computed as the
  **sum of haversine arcs over the emitted (rounded) coordinates**, using the
  identical earth radius the Go loader uses (`engine/internal/graph/geo.go`,
  6 371 000 m). A polyline of great-circle arcs is never shorter than the direct
  arc between its ends, so this *guarantees* `length_m >= chord` — and for a
  straight 2-point edge it matches the loader's chord to within a few ULPs (well
  inside the loader's `1e-9` relative tolerance, since Python and Go group the
  haversine terms slightly differently). `length_m` is
  emitted **unrounded** on purpose: rounding it (even to 6 dp) can push a straight
  edge a fraction of a micrometer below its chord and trip the guard. Because of
  this, **the engine's `lengthChordRelTol` did not need to be widened** — the real
  Porto extract loads with the loader untouched.

- **Coordinate precision.** Coordinates are emitted at 7 decimal places (~1 cm),
  matching the loader's `nodePosEpsilonDeg = 1e-7`; two edges sharing a pgRouting
  vertex carry that vertex's identical coordinate, so the loader's re-used-node
  reconciliation passes.

## Verification (issue #120 acceptance)

On the committed run (bbox `41.14,-8.63 .. 41.165,-8.595`), the pipeline loaded
**3723 directed edges / 2198 nodes** through `LoadEdgeAttributesGeoJSON` with
**zero engine changes**; all 3723 `segment_id`s round-trip through
`domain.ParseSegmentID`; and `capacity_vph` / `freeflow_time_s` / `class_factor`
match the §2 tables for every present class (primary, secondary, tertiary,
residential, service — central Porto has no motorway/trunk). The load emits a
non-fatal connectivity WARN (bbox-clipped boundary edges form small islands),
which is a diagnostic, not a rejection.

## Reproducibility

The scripts are deterministic **given a fixed `data/osm/porto.osm`**: re-running the
load+export on the same extract reproduces the same edges and `segment_id`s (§1's
"stable across re-imports" guarantee). Step 1, however, fetches **live** OpenStreetMap
via Overpass, which is continuously edited — a re-fetch weeks later may return
different geometry, way ids, sub-segment splits, and edge count than the committed
run's 3723. To reproduce an exact prior artifact (e.g. to re-run a benchmark against
it), keep the extract and re-run with `SKIP_FETCH=1` rather than re-fetching.

## Testing

`test_export.py` conformance-checks the pure §2 derivation logic (`build_features` +
the helpers) against the golden fixture `docs/fixtures/edge_attributes/example_export.json`
— no Postgres/Docker/Overpass needed (the exporter imports `psycopg2` lazily):

```sh
cd data/scripts && python3 -m unittest test_export
```

## Env overrides

| Var | Default | Meaning |
|-----|---------|---------|
| `PORTO_BBOX` | `41.1400,-8.6300,41.1650,-8.5950` | `south,west,north,east` fetch box |
| `SKIP_FETCH` | `0` | `1` reuses an existing `data/osm/porto.osm` |
| `PG_PORT` | `5432` | host port the compose Postgres publishes on |
| `OVERPASS_URL` | `https://overpass-api.de/api/interpreter` | Overpass endpoint |

## Files

- `fetch_porto_osm.sh` — Overpass fetch of the bounded Porto extract.
- `Dockerfile` — `cra/data-tools:phase8` (osm2pgrouting + `postgresql-client` + python3 + psycopg2).
- `run_pipeline.sh` — the end-to-end orchestrator (`make data-porto`).
- `export_edge_attributes.py` — the §1/§2 exporter (pgRouting topology + raw tags).
- `test_export.py` — stdlib `unittest` conformance test for the §2 derivations.
