# Frozen Cross-Team Contracts

> The three contracts the board froze at kickoff (`project-spec.md §R0`). This file is the single source
> of truth shared by the database, data-pipeline, and routing-engine teams. **Do not change a frozen
> contract without bumping its version and notifying all consumers.**

## 1. Canonical `segment_id`

**Contract owner:** Database / Geospatial Engineer.
**Contract version: 1.**
**Consumers / depended-on-by:** the `edge_attributes` export (#4, §2) and the `segment-congestion` message
(#5, §3), and later the frontend.
**Conformance status:** proven against Go (engine); PySpark (pipeline, #5) pending.

`segment_id` is the single cross-system **wire key** for a directed piece of road. It is the only road
identifier shared by the database export, the Spark map-matcher/pipeline, and the Go engine. If these three
disagree on what a segment is called, congestion silently attaches to the wrong road (or to nothing) and every
downstream number is garbage — so this is the most important frozen contract in the project.

### Scheme

```
segment_id = "{osm_way_id}:{seq}:{dir}"
```

Three colon-separated fields, exactly two colons. Example: `"123456789:0:F"`.

| Field        | Type            | Definition |
|--------------|-----------------|------------|
| `osm_way_id` | int64, positive | The OpenStreetMap **way** id, taken from `ways.osm_id`. Stable across re-imports of the same OSM extract. |
| `seq`        | int, >= 0       | 0-based ordinal of this sub-segment within the way, after pgRouting splits the way at intersections. A way that crosses two intersections yields sub-segments `seq = 0, 1, 2`. |
| `dir`        | `F` or `R`      | Travel direction. `F` (forward) runs **along** the way's node ordering; `R` (reverse) runs against it. Case-sensitive, exactly one letter. |

### Directed rule

`segment_id` is **directed** — it names a direction of travel, not just a stretch of road:

- A **two-way** street is **two** segment_ids that share `osm_way_id` and `seq` and differ only in `dir`:
  `…:F` and `…:R`.
- A **one-way** street is **one** segment_id: only its permitted direction exists.
- The map-matcher resolves which direction a GPS trace is travelling from its heading and emits the matching
  `dir`.

### `gid`/`id` is BANNED as a wire key — and why

The wire key is built from `osm_way_id` (`ways.osm_id`), **never** from the `osm2pgrouting`
auto-increment `gid`/`id` columns. Those auto-increment surrogate keys are forbidden in any `segment_id`
because:

1. **They are non-deterministic across re-imports.** Re-running the import (new OSM extract, different load
   order, a schema rebuild) renumbers `gid`/`id`. The same physical road would get a different id, so
   congestion computed against an old export would land on the wrong road after any reload.
2. **They are unknown outside PostGIS.** The Spark map-matcher matches GPS pings to OSM geometry by
   `osm_id`; it never sees the database's internal row numbers. Only `osm_id` is computable by all three
   systems independently.

`osm_id` is assigned by OpenStreetMap and is stable for a given extract, so all three systems can derive the
identical `segment_id` without coordinating through the database's internal counters.

### `segment_id` ↔ `EdgeID`

There are two identifiers for an edge, with distinct jobs:

- **`segment_id`** (this contract) — the stable, directed, cross-system **wire key**. It travels between
  systems: in the `edge_attributes` export, on the `segment-congestion` Kafka topic, and in the API. It is a
  string and is never used on hot in-memory paths.
- **`EdgeID`** (`engine/internal/domain`) — a compact `int32` the engine assigns at load time, dense and
  contiguous `0..EdgeCount-1`, used to directly index flat per-edge slices.

At graph build time the engine constructs a **lookup** in both directions
(`segment_id → EdgeID` and `EdgeID → segment_id`). Inbound congestion keyed by `segment_id` is translated to
an `EdgeID` once, at ingest; everything on the hot path uses `EdgeID`. `EdgeID` values are an in-memory
detail of one process and MUST NOT appear on the wire.

### Reference implementation & golden fixtures

- **Go:** `FormatSegmentID` / `ParseSegmentID` in `engine/internal/domain/segmentid.go`. Parsing is strict
  (see below).
- **Fixtures (language-neutral, shared):** `docs/fixtures/segment_id/` —
  `format_cases.json` (valid rows that must round-trip) and `parse_invalid.json` (malformed strings that
  must be rejected). Both the Go engine and the (future) PySpark pipeline load these **same** files so the
  two implementations cannot drift. See that directory's `README.md`.

A conformant **parser is strict** and rejects anything that is not exactly the scheme: it requires exactly
two colons (three fields); `osm_way_id` must be a canonical positive base-10 integer (`>= 1`) and `seq` a
canonical non-negative base-10 integer (`>= 0`), both with no sign, no leading zeros, no whitespace, and no
hex; and `dir` must be exactly `F` or `R`. It returns a descriptive error and never panics.

### Worked examples

1. **One-way way.** OSM way `27583001` is a one-way street that pgRouting did not split (single sub-segment).
   It produces exactly one segment_id:
   - `"27583001:0:F"`
2. **Two-way way → two ids.** OSM way `48800123` is a two-way street, one sub-segment. It produces a pair
   sharing way+seq, differing only in `dir`:
   - `"48800123:0:F"` (with the node ordering)
   - `"48800123:0:R"` (against the node ordering)
3. **Multi-segment way split at an intersection → multiple `seq`.** OSM way `905512` crosses two
   intersections, so pgRouting splits it into three sub-segments `seq = 0, 1, 2`. Forward direction:
   - `"905512:0:F"`, `"905512:1:F"`, `"905512:2:F"`

   And because it is two-way, each sub-segment also has its reverse, e.g. `"905512:2:R"`.

### Versioning

This is **Contract version: 1**. The scheme above is frozen. **Changing it — the field set, their order,
the delimiter, the `dir` tokens, or the strict-parsing rules — requires bumping the contract version here
and notifying every consumer (database export, pipeline, engine, frontend) so they update in lockstep.** The
shared fixtures in `docs/fixtures/segment_id/` are part of this contract and must be updated in the same
change; do not edit a fixture to paper over a non-conformant implementation.

Because `segment_id` carries **no in-wire version token** of its own, the `edge_attributes` (§2) and
`segment-congestion` (§3) **envelope schema versions are its operational version-carriers**: a §1 format
change REQUIRES bumping those envelope schema versions too, so consumers reading those envelopes can detect
the change.

## 2. `edge_attributes` export schema

**Contract owner:** Database / Geospatial Engineer.
**Contract version: 1.**
**Consumers / depended-on-by:** the Go routing engine's graph loader (#9, `engine/internal/graph`) and the
frontend `/graph` endpoint (later).
**Conformance status:** schema + derivation rules frozen here; Go loader and frontend consumers pending.

`edge_attributes` is the immutable road-network snapshot the engine runs on. It is **one row per directed
edge** — the two directions of a two-way street are two rows; a one-way street is one row. It is produced by
`data/scripts` from the PostGIS/pgRouting build and carries everything the engine needs to evaluate BPR cost:
identity, topology, geometry, and the derived capacity / free-flow fields. The engine loads this artifact once
at startup into an immutable in-memory graph and **never queries Postgres at request time.** The frontend's
per-segment geometry comes from the *same* artifact (served as a GeoJSON `/graph` endpoint), so coloring a road
by congestion is a pure `segment_id` join — there is no second source of geometry to drift from.

The `segment_id` field conforms to §1 and MUST NOT be redefined locally.

### Columns

One row per directed edge, with exactly these 12 columns:

| Column            | Type                  | Definition |
|-------------------|-----------------------|------------|
| `segment_id`      | string                | The §1 canonical wire key `"{osm_way_id}:{seq}:{dir}"`. The durable, cross-system identity of this directed edge. Conforms to §1 exactly; see §1 for the scheme and strict-parsing rules. |
| `edge_id`         | int32, `>= 0`         | The engine's compact dense edge index (§1 `EdgeID`), `0..EdgeCount-1`. Materialized in the export so the Parquet and the GeoJSON `/graph` agree on the *same* integer for a row (see "edge_id is the load-time assignment" below). |
| `source_node`     | int32, `>= 0`         | The §1 `NodeID` of this directed edge's tail (the `From` vertex). |
| `target_node`     | int32, `>= 0`         | The §1 `NodeID` of this directed edge's head (the `To` vertex). |
| `osm_way_id`      | int64, positive       | The OpenStreetMap way id this edge came from. MUST equal the `osm_way_id` embedded in `segment_id` (self-consistency rule below). |
| `highway_class`   | string (enum)         | OSM `highway` tag, one of: `motorway`, `trunk`, `primary`, `secondary`, `tertiary`, `residential`, `service`. Drives the default and `class_factor` tables below. |
| `lanes_effective` | int, `>= 1`           | Lanes available **in this direction** (see derivation). |
| `length_m`        | float64, meters, `> 0`| Geodesic length of the edge geometry, in meters. |
| `maxspeed_kmh`    | float64, km/h, `> 0`  | Free-flow speed limit in km/h (see derivation). |
| `freeflow_time_s` | float64, seconds, `> 0`| Free-flow traversal time in seconds: `length_m / (maxspeed_kmh → m/s)` (see derivation). |
| `capacity_vph`    | float64, veh/hour, `> 0`| BPR capacity `c` in **vehicles per hour** (see derivation and the unit contract). |
| `geometry`        | LineString            | The directed edge geometry. Coordinate order is **`[lon, lat]`** (GeoJSON / x,y), drawn in the edge's travel direction (`source_node` first, `target_node` last). |

These map onto the engine's in-memory `graph.Edge` (`engine/internal/graph/graph.go`) as:
`edge_id`→`ID`, `segment_id`→`Segment`, `source_node`→`From`, `target_node`→`To`, `length_m`→`LengthM`,
`freeflow_time_s`→`FreeFlowS`, `capacity_vph`→`CapacityVPH`. (`osm_way_id`, `highway_class`,
`lanes_effective`, `maxspeed_kmh` are derivation inputs the in-memory struct does not retain; `geometry` is
held separately for map-matching and the `/graph` endpoint.)

#### `edge_id` is the load-time assignment

Mirroring §1's `segment_id ↔ EdgeID` framing: `segment_id` is the durable cross-system wire key; `edge_id`
is the compact in-memory index. §1 notes the engine assigns `EdgeID` densely at load. In the **export**,
`edge_id` is **materialized** — written into both the Parquet and the GeoJSON — so the two serializations of
the same snapshot share one integer per edge and the frontend `/graph` join lines up with the engine's slices.
The engine MAY reassign `EdgeID`s when it loads (it owns its in-memory layout), but the export's `edge_id` is
the load-time assignment the engine adopts for *this* snapshot. As in §1, `edge_id` is an in-memory/in-artifact
index, never a cross-system identity: `segment_id` remains the only key on the `segment-congestion` wire (§3).

### Derivation rules

OSM tags are sparse, so capacity and free-flow are **derived per directed edge** from `highway_class` with
defaults, never hand-tuned. A conformant exporter MUST reproduce these exactly.

**`lanes_effective`** — OSM `lanes` for this direction if tagged, else the class default:

| highway_class | lanes default |
|---------------|---------------|
| motorway      | 3 |
| trunk         | 2 |
| primary       | 2 |
| secondary     | 2 |
| tertiary      | 1 |
| residential   | 1 |
| service       | 1 |

**`maxspeed_kmh`** — OSM `maxspeed` if tagged, else the class default:

| highway_class | maxspeed default (km/h) |
|---------------|-------------------------|
| motorway      | 100 |
| trunk         | 80 |
| primary       | 60 |
| secondary     | 50 |
| tertiary      | 40 |
| residential   | 30 |
| service       | 20 |

**`class_factor`** — a monotonic ramp from motorway `1.0` down to `service`, applied in the capacity formula.
A motorway lane discharges near the `1800 veh/h/lane` saturation flow; lower classes have more friction
(driveways, parking, pedestrians) and discharge proportionally less:

| highway_class | class_factor |
|---------------|--------------|
| motorway      | 1.0 |
| trunk         | 0.9 |
| primary       | 0.8 |
| secondary     | 0.7 |
| tertiary      | 0.6 |
| residential   | 0.5 |
| service       | 0.4 |

**`capacity_vph`** — saturation flow ≈ `1800 veh/h/lane`:

```
capacity_vph = lanes_effective × 1800 × class_factor × capacity_scale
```

`capacity_scale` is one **global** multiplier — the frontend's single "tunable" knob for sensitivity sweeps.
The export is generated at `capacity_scale = 1.0` (the values in the column and the fixtures assume `1.0`);
the engine/frontend applies any other scale at runtime. Do not bake a non-1.0 scale into the export.

**`freeflow_time_s`** — free-flow traversal time, in seconds, from length and speed limit:

```
freeflow_time_s = length_m / (maxspeed_kmh × 1000 / 3600)
                = length_m / (maxspeed_kmh / 3.6)
```

Units: `length_m` is meters, `maxspeed_kmh` is km/h; `maxspeed_kmh × 1000 / 3600` converts km/h → m/s, so the
quotient is seconds. (e.g. `60 km/h = 16.6667 m/s`; a `240 m` edge → `240 / 16.6667 = 14.4 s`.)

### Unit contract (BPR flow units)

BPR cost is `t = freeflow_time_s × (1 + α·(v/c)^β)`. Both `v` (assigned flow) and `c` (= `capacity_vph`) **MUST
be in vehicles per hour.** This is a *written* contract because a mismatch does not crash — it silently
corrupts the headline number, and with `β = 4` the error is raised to the fourth power.

Spark emits `vehicle_count` per **5-minute** window (§3). The congestion adapter **annualizes that count to an
hourly rate before BPR**: `v_vph = vehicle_count × 12` (twelve 5-minute windows per hour). `capacity_vph` in
this export is already vehicles/hour, so once the adapter applies `× 12`, `v` and `c` are in the same unit and
`v/c` is dimensionless. The `× 12` annualization is the adapter's responsibility (engine side), but it is
stated here because it is the other half of the unit contract that makes `capacity_vph` meaningful.

### Serializations

The same logical rows are emitted in two formats, and they MUST carry identical values per `segment_id`:

1. **Parquet** (`edge_attributes.parquet`) — for the engine. One record per directed edge, columns as in the
   table above. `geometry` is stored as a serialized LineString (WKB/GeoJSON-string per the writer); all other
   columns are the scalar types listed.
2. **GeoJSON `FeatureCollection`** (`edge_attributes.geojson`) — for the frontend `/graph` endpoint. One
   `Feature` per directed edge:
   - `Feature.geometry` is the `LineString` with coordinates in **`[lon, lat]`** order (GeoJSON x,y), in the
     edge's travel direction.
   - **Every other column** (`segment_id`, `edge_id`, `source_node`, `target_node`, `osm_way_id`,
     `highway_class`, `lanes_effective`, `length_m`, `maxspeed_kmh`, `freeflow_time_s`, `capacity_vph`) goes
     into `Feature.properties` under the same key. The frontend colors a road by joining its live congestion
     to `properties.segment_id` — a pure §1 join, no geometry recomputation.

### Reference / fixtures

- **Golden fixture (language-neutral, shared):** `docs/fixtures/edge_attributes/example_export.json` — a small
  array of directed-edge rows with all 12 columns, where every `segment_id` is valid under §1 and
  `capacity_vph` / `freeflow_time_s` are computed from the rules above (so a conformant exporter must
  reproduce them). See that directory's `README.md`.
- The `segment_id` rules and their own fixtures live in §1 / `docs/fixtures/segment_id/`; this section never
  redefines them.

### Versioning

This is **Contract version: 1**. The column set, their types, the derivation rules (default tables,
`class_factor`, the capacity and free-flow formulas), the unit contract, and both serializations are frozen.
**Changing any of them — including adding/removing a column, retuning a default or `class_factor`, or changing
the GeoJSON property mapping — requires bumping the contract version here and notifying every consumer (engine
loader, frontend) so they update in lockstep.** The fixture in `docs/fixtures/edge_attributes/` is part of this
contract; do not edit it to paper over a non-conformant exporter.

As §1 notes, `segment_id` carries no in-wire version token, so **this `edge_attributes` envelope's schema
version is one of `segment_id`'s operational version-carriers**: a §1 format change REQUIRES bumping this
contract version too, so consumers reading this artifact can detect the change.

## 3. `segment-congestion` schema v2 — *to be filled by issue #5*

Event-time windowed congestion records on a compacted Kafka topic keyed by `segment_id`.

The `segment_id` field conforms to §1 and MUST NOT be redefined locally.
