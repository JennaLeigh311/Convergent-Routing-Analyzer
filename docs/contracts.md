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
**Consumers / depended-on-by:** the Go routing engine's graph loader (future, `engine/internal/graph`) and
the frontend `/graph` endpoint (later).
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
| `source_node`     | int32                 | An arbitrary `int32` vertex reference for this directed edge's tail (the `From` vertex), consistent **within** this export — edges sharing a vertex MUST use the same ref, so shared endpoints fuse. It is **not** dense in the export (it MAY be sparse) and is never a durable/cross-snapshot key; the engine compacts it to a dense `0..NodeCount-1` `NodeID` at load — see "node ids are a load-time assignment" below. |
| `target_node`     | int32                 | An arbitrary `int32` vertex reference for the head (the `To`) vertex; same governance as `source_node`. |
| `osm_way_id`      | int64, positive       | The OpenStreetMap way id this edge came from. MUST equal the `osm_way_id` embedded in `segment_id` (self-consistency rule below). |
| `highway_class`   | string (enum)         | OSM `highway` tag, one of exactly: `motorway`, `trunk`, `primary`, `secondary`, `tertiary`, `residential`, `service`. Drives the default and `class_factor` tables below. The exporter MUST map every edge into one of these seven (collapsing OSM `*_link`/variant tags into their base class); a value outside the enum has no derivation rule, so a loader MAY reject it. |
| `lanes_effective` | int, `>= 1`           | Lanes available **in this direction** (see derivation). |
| `length_m`        | float64, meters, `> 0`| Geodesic length of the edge geometry, in meters. |
| `maxspeed_kmh`    | float64, km/h, `> 0`  | Free-flow speed limit in km/h (see derivation). |
| `freeflow_time_s` | float64, seconds, `> 0`| Free-flow traversal time in seconds: `length_m / (maxspeed_kmh → m/s)` (see derivation). |
| `capacity_vph`    | float64, veh/hour, `> 0`| BPR capacity `c` in **vehicles per hour** (see derivation and the unit contract). |
| `geometry`        | LineString            | The directed edge geometry. Coordinate order is **`[lon, lat]`** (GeoJSON / x,y), drawn in the edge's travel direction (`source_node` first, `target_node` last). A LineString MAY have more than two coordinates: **intermediate coordinates are geometry shape points only — they are not graph nodes.** Only the **first** and **last** coordinates correspond to `source_node` / `target_node`; a loader must not treat interior vertices as routable nodes. |

These map onto the engine's in-memory `graph.Edge` (`engine/internal/graph/graph.go`) as:
`edge_id`→`ID`, `segment_id`→`Segment`, `source_node`→`From`, `target_node`→`To`, `length_m`→`LengthM`,
`freeflow_time_s`→`FreeFlowS`, `capacity_vph`→`CapacityVPH`. (`osm_way_id`, `highway_class`,
`lanes_effective`, `maxspeed_kmh` are derivation inputs the in-memory struct does not retain; `geometry` is
held separately for map-matching and the `/graph` endpoint.)

#### Implied invariant: `length_m >= endpoint great-circle chord`

`length_m` is the **geodesic length of the edge geometry**, and a geodesic is by definition at least as long
as the straight line between its ends. So `length_m` MUST be `>= ` the great-circle (haversine) chord between
the edge's first and last geometry coordinates (`source_node` / `target_node` positions). This is not a new
field — it is a **mathematical entailment** of "geodesic length" already in the table — but the Go loader now
**enforces** it: a row whose `length_m` is shorter than its endpoint chord (beyond a `1e-9` relative
tolerance that absorbs float rounding only, not real producer rounding) is rejected at load
(`engine/internal/graph/loader.go`). It is measured on the **endpoints only** (first/last coordinate), never
the interior shape points or the polyline length. Both the producer (database export) and the frontend
`/graph` consumer therefore share this expectation: a length shorter than the endpoint chord is a
contradictory export. The producer MUST emit `length_m` at a precision consistent with the coordinate
precision it derives it from, so a legitimately straight road (`length_m == chord`) is not pushed below the
tolerance by rounding.

This enforcement does **not** bump the frozen contract version: enforcing an entailment of an existing field
is not a field-set, type, derivation, unit, or serialization change, so version **1** stands.

#### `edge_id` is the load-time assignment

Mirroring §1's `segment_id ↔ EdgeID` framing: `segment_id` is the durable cross-system wire key; `edge_id`
is the compact in-memory index. §1 notes the engine assigns `EdgeID` densely at load. In the **export**,
`edge_id` is **materialized** — written into both the Parquet and the GeoJSON — so the two serializations of
the same snapshot share one integer per edge and the frontend `/graph` join lines up with the engine's slices.
The engine MAY reassign `EdgeID`s when it loads (it owns its in-memory layout), but the export's `edge_id` is
the load-time assignment the engine adopts for *this* snapshot. As in §1, `edge_id` is an in-memory/in-artifact
index, never a cross-system identity: `segment_id` remains the only key on the `segment-congestion` wire (§3).

**`edge_id` is not a join key.** Within one snapshot the Parquet and the GeoJSON carry the *identical*
`edge_id` per edge, so a tool reading both files together MAY pair rows by it. But the durable join key — the
frontend `/graph` ↔ live-congestion join, and any join that must survive a re-export or the engine renumbering
its in-memory slices — is **`segment_id`**. A consumer MUST NOT use `edge_id` as a cross-serialization or
cross-snapshot join key unless it is pairing the two files of one specific export.

#### `source_node` / `target_node` are a load-time assignment too

Node ids differ from `edge_id` in **who** assigns them. The export materializes `source_node`/`target_node`
as **arbitrary `int32` vertex references**: they need only be self-consistent **within one export** — edges
sharing a vertex MUST carry the same ref so shared endpoints fuse — and they MAY be **sparse** (e.g. raw
pgRouting vertex ids). They are **not** required to be dense. The **engine** assigns the compact, dense,
contiguous `0..NodeCount-1` `NodeID` (`engine/internal/domain`, `int32`) at **load** time, by deterministic
compaction: grouping rows by export ref, then sorting by the export ref so the assignment is stable, and
sizing a flat per-node slice directly (reference: `engine/internal/graph/loader.go`). Like `edge_id`, node
ids are an **in-artifact/in-memory index, not a cross-system identity**: they are not stable across a
re-export (a new build MAY renumber them) and MUST NOT be used as a durable or cross-snapshot key — the only
durable identity remains `segment_id` (for edges) and the geometry endpoints (for node position). The OSM node
id a vertex came from is not part of this contract; if a future consumer needs stable node identity it must be
added here under a version bump.

This is **different** from `edge_id` above: `edge_id` is **materialized-and-adopted** dense — the export
writes the dense index and the engine adopts it for the within-snapshot Parquet↔GeoJSON pairing — whereas
`source_node`/`target_node` carry arbitrary refs that the **engine** compacts to dense `NodeID`s at load.

### Derivation rules

OSM tags are sparse, so capacity and free-flow are **derived per directed edge** from `highway_class` with
defaults, never hand-tuned. A conformant exporter MUST reproduce these exactly.

**`lanes_effective`** — lanes available **in this one direction of travel**, resolved in this precedence
(this matters: OSM `lanes` is conventionally the *both-directions total*, so applying it whole to each
direction would double a two-way street's capacity — and with `β = 4` that silently corrupts the headline
number):

1. If the direction-specific tag is present (`lanes:forward` for the `F` row, `lanes:backward` for the `R`
   row), use it directly.
2. Else, if the edge is **one-way**, use the bare `lanes` tag (it already describes the single direction).
3. Else (two-way, only a bare `lanes` tagged), the bare `lanes` is the both-directions total: split it across
   directions — `floor(lanes / 2)`, clamped to a minimum of `1`.
4. Else (no `lanes` tag at all), use the class default below.

Class default when no usable tag exists:

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
corrupts the headline number, and with `β = 4` the error is raised to the fourth power. (The coefficients `α`
and `β` themselves are *not* part of this export contract — they are pinned by the engine's BPR
`CostFunction`, default `α = 0.15`, `β = 4`; this section only fixes the **units** of `v` and `c` that flow
into it.)

Spark emits `vehicle_count` per **5-minute** window (§3). The congestion adapter **scales that count to an
hourly rate before BPR**: `v_vph = vehicle_count × 12` (twelve 5-minute windows per hour). `capacity_vph` in
this export is already vehicles/hour, so once the adapter applies `× 12`, `v` and `c` are in the same unit and
`v/c` is dimensionless. The `× 12` hourly scaling is the adapter's responsibility (engine side), but it is
stated here because it is the other half of the unit contract that makes `capacity_vph` meaningful.

### Serializations

The same logical rows are emitted in two formats, and they MUST carry identical values per `segment_id`:

1. **Parquet** (`edge_attributes.parquet`) — for the engine. One record per directed edge, columns as in the
   table above. `geometry` is stored as **WKB** (well-known binary) `LineString` — a single fixed encoding so a
   Go loader decodes it without out-of-band knowledge (do **not** emit a GeoJSON string or WKT here). WKB
   carries no CRS or axis convention of its own, so this contract pins both: **axis order `(X = lon, Y = lat)`
   and CRS EPSG:4326 (WGS84)** — identical to the GeoJSON `[lon, lat]` rule, so a decoder must never swap
   axes. All other columns are the scalar types listed.
2. **GeoJSON `FeatureCollection`** (`edge_attributes.geojson`) — for the frontend `/graph` endpoint. One
   `Feature` per directed edge:
   - `Feature.geometry` is the `LineString` with coordinates in **`[lon, lat]`** order (GeoJSON x,y), in the
     edge's travel direction.
   - **Every other column** (`segment_id`, `edge_id`, `source_node`, `target_node`, `osm_way_id`,
     `highway_class`, `lanes_effective`, `length_m`, `maxspeed_kmh`, `freeflow_time_s`, `capacity_vph`) goes
     into `Feature.properties` under the same key. The frontend colors a road by joining its live congestion
     to `properties.segment_id` — a pure §1 join, no geometry recomputation.

#### Envelope `schema_version`

Both artifacts carry an **envelope-level `schema_version`** equal to this section's contract version (currently
`1`). It is **not** a 13th per-row column — it is one value attached to the whole artifact, so a consumer can
detect a schema change at load time before trusting the rows:

- **Parquet:** a file-level key/value metadata entry `schema_version = "1"` (Parquet footer KV-metadata).
- **GeoJSON:** a top-level member `"schema_version": 1` on the `FeatureCollection` object (alongside `"type"`
  and `"features"`).

This is what makes the version-carrier rule below mechanical rather than a documentation promise.

### Reference / fixtures

- **Golden fixture (language-neutral, shared):** `docs/fixtures/edge_attributes/example_export.json` — a small
  array of directed-edge rows with all 12 columns, where every `segment_id` is valid under §1 and
  `capacity_vph` / `freeflow_time_s` are computed from the rules above (so a conformant exporter must
  reproduce them). Each fixture row also carries a non-contract `note` field describing what it exercises; it
  is documentation only and is not part of the schema. See that directory's `README.md`.
- The `segment_id` rules and their own fixtures live in §1 / `docs/fixtures/segment_id/`; this section never
  redefines them.

### Versioning

This is **Contract version: 1**. The column set, their types, the derivation rules (default tables,
`class_factor`, the capacity and free-flow formulas), the unit contract, and both serializations are frozen.
**Changing any of them — including adding/removing a column, retuning a default or `class_factor`, or changing
the GeoJSON property mapping — requires bumping the contract version here and notifying every consumer (engine
loader, frontend) so they update in lockstep.** The fixture in `docs/fixtures/edge_attributes/` is part of this
contract; do not edit it to paper over a non-conformant exporter.

As §1 notes, `segment_id` carries no in-wire version token, so **this `edge_attributes` envelope's
`schema_version` is one of `segment_id`'s operational version-carriers**: a §1 format change REQUIRES bumping
this contract version (and therefore the `schema_version` written into both artifacts) too, so consumers
reading the Parquet footer metadata or the GeoJSON top-level `schema_version` can detect the change at load
time rather than silently mis-parsing.

## 3. `segment-congestion` schema v2

**Contract owner:** Data Pipeline Engineer.
**Contract version: 1.**
**Consumers / depended-on-by:** the Go routing engine's congestion adapter and its static replayer
(`engine/internal/...`, future), and the frontend live-congestion overlay (later).
**Conformance status:** message schema frozen here; the Go adapter + static replayer and the PySpark
producer are pending.

`segment-congestion` is the live congestion feed flowing from the pipeline (producer) to the engine
(consumer): **one JSON object per Kafka message** on the `segment-congestion` topic, each carrying the
per-segment, per-time-window vehicle aggregate plus the bookkeeping fields that make late and updated data
correct. It is frozen **before the pipeline exists** because the engine develops its congestion adapter
against a fake/simulated source first; if the future Spark producer and the engine disagree on this shape,
congestion silently attaches to the wrong window or the wrong road and every downstream number — the headline
"26% improvement" — is computed against garbage without anything crashing. That silent-and-catastrophic
failure mode is why this is a written, frozen contract with MUST/MUST NOT rules, not a convention.

The `segment_id` field conforms to §1 and MUST NOT be redefined locally; it is the §1 wire key and the Kafka
**message key** on this topic.

### Why "v2"

"v2" is the **schema generation**, not the in-wire version number. The original v1 design was a flat
per-segment count with no notion of event time and no handling of late or revised data. **v2** is the
redesign that adds event-time windowing, watermarks, and the late-data / dedup bookkeeping (`window_start`,
`window_end`, `is_final`, `emit_time`) documented below. The in-wire version carried on each message is
`schema_version`, and the first frozen version of the v2 envelope is **`schema_version: 1`**. So a message on
this topic reads `"schema_version": 1` even though the contract is named "v2": "v2" names *which generation of
the schema design* this is; `schema_version: 1` is *the first frozen revision of that generation's wire
envelope*. They are independent counters and MUST NOT be conflated.

### Message shape

One JSON object per message, exactly these 10 fields, in this order:

```json
{ "schema_version": 1, "segment_id": "123456789:2:F",
  "window_start": "2026-06-08T08:00:00Z", "window_end": "2026-06-08T08:05:00Z",
  "vehicle_count": 42, "avg_speed_kmh": 18.3, "sample_pings": 137,
  "is_final": false, "emit_time": "2026-06-08T08:05:03Z", "producer": "spark-structured-stream" }
```

### Fields

| Field            | Type                       | Definition |
|------------------|----------------------------|------------|
| `schema_version` | int, currently `1`         | Envelope-level wire version of this message schema. Currently `1` (the first frozen v2 envelope — see "Why 'v2'" above). It is **also the operational version-carrier for the §1 `segment_id` format on this topic**: a §1 format change REQUIRES bumping this (see "Versioning"), so a consumer can detect the change at ingest before mis-parsing the key. Not to be confused with the "v2" schema generation. |
| `segment_id`     | string                     | The §1 canonical wire key `"{osm_way_id}:{seq}:{dir}"` and the **Kafka message KEY** on this topic. Conforms to §1 exactly; see §1 for the scheme and strict-parsing rules. MUST NOT be redefined locally. |
| `window_start`   | string, RFC3339 UTC        | **Event-time** lower bound of the aggregation window, inclusive. **Canonical wire form:** RFC3339 with a literal `Z` (UTC) offset, whole-second precision, no fractional digits — so the dedup comparison is well-defined (see "Dedup rule"). |
| `window_end`     | string, RFC3339 UTC        | **Event-time** upper bound of the window, **exclusive**. The window is half-open `[window_start, window_end)`. `window_end − window_start` is **exactly 5 minutes** — the window length (see "Windowing & streaming semantics"). |
| `vehicle_count`  | int, `>= 0`                | Count of **distinct vehicles** observed on this segment during this window. This is a **per-5-minute-window count, NOT an hourly rate.** The engine's congestion adapter scales it to vehicles/hour before BPR as `v_vph = vehicle_count × 12` (twelve 5-min windows per hour) — see "Unit contract" and §2's unit contract. |
| `avg_speed_kmh`  | float64, km/h, `> 0`       | Mean map-matched vehicle speed over the window, in km/h. A diagnostic/heatmap field (the engine's cost is driven by `vehicle_count`/capacity via BPR, not by this speed). |
| `sample_pings`   | int, `>= 0`                | Count of raw GPS pings that contributed to this aggregate. Typically `>= vehicle_count`, since one vehicle emits many pings over a 5-minute window. Diagnostic/confidence field — a window with few pings behind its `vehicle_count` is lower-confidence. |
| `is_final`       | bool                       | `false` = a **provisional** emission for a window that may still receive more (late) pings before the watermark passes — overwritable by a later emission for the same window (see dedup rule). `true` = the window has closed past the watermark and will **not** be updated; this is the last record for the window. |
| `emit_time`      | string, RFC3339 UTC        | Wall-clock (processing-time) instant the producer emitted **this** record. Same **canonical wire form** as `window_start` (literal `Z`, whole-second precision, no fractional digits). Used purely for **dedup ordering** (see the dedup rule); it is producer wall-clock, distinct from the event-time `window_start`/`window_end`. |
| `producer`       | string                     | Identifier of the emitting job, e.g. `"spark-structured-stream"`. The **same** value for the live and the batch run modes, because it is one job run two ways (see "Windowing & streaming semantics"). |

### Dedup rule (latest `(window_start, emit_time)` wins)

The engine keeps, per `segment_id`, exactly **one** current record: the one with the **latest
`(window_start, emit_time)`**, ordered by `window_start` first and `emit_time` only as the tiebreaker —

1. **Newest window wins.** A record with a greater `window_start` supersedes any record for an earlier window
   on the same segment.
2. **Within the same window, the latest `emit_time` wins.** Two records sharing `segment_id` *and*
   `window_start` are ordered by `emit_time`; the later one supersedes the earlier.

Consequently an `is_final: false` (provisional) record is **overwritable** by any later emission for the same
window — whether a higher-count provisional revision or the eventual `is_final: true` record. The two
timestamps being compared have **different clocks** — `window_start` is event-time, `emit_time` is producer
wall-clock (processing-time) — but both are emitted in the **canonical wire form** required by the field table
(RFC3339, a literal `Z` offset, whole-second precision, no fractional digits). A conformant consumer MUST
apply this ordering by comparing the timestamps **as parsed instants**; it MAY instead byte-compare them
lexicographically, which is equivalent **only because** the canonical form is fixed-width and `Z`-normalized
(a consumer MUST NOT byte-compare a timestamp it has not validated to that form — e.g. a `+00:00` offset,
lowercase `z`, or fractional seconds would sort wrongly). A conformant consumer MUST NOT, e.g., sum
successive emissions for one window (that would double-count the revisions).

**Sliding windows replace, they do not accumulate.** Because the windows slide (5-min length, 1-min slide —
see "Windowing & streaming semantics"), successive windows on one segment **overlap** and the same ping is
counted in several of them. The dedup rule keeps exactly **one** record per segment — the latest
`window_start` — as that segment's current load, and **discards** the overlapping earlier windows; a consumer
MUST NOT sum or otherwise combine two overlapping windows (their counts share pings, so adding them
double-counts). Note a direct consequence: under a 1-min slide the latest-`window_start` record is normally
still **provisional** (`is_final: false`) — it is the freshest window and has had the least time to absorb
late pings — and that is intended: "freshest window wins" deliberately prefers currency over finality for the
live load estimate. `is_final` records still arrive (for dedup within a window and for the batch pass); they
do **not** override a later window merely for being final, because rule 1 (`window_start`) is the primary
sort key (see fixture rows 3→4).

One consequence is **accepted explicitly**: the freshest window is also the **least-settled** one — having
slid forward only a minute, part of its span still falls within the 2-min watermark horizon, so its
`vehicle_count` is a partial, still-filling count that feeds directly into BPR `v`. This is a deliberate bias
of the *live* path (a real-time load estimate favors currency over completeness), not a defect. A consumer
that needs a fully-settled load instead — the batch / static-replay path — reads the `is_final: true` records,
which are emitted only after the watermark closes the window.

### Windowing & streaming semantics

The producer is a **Spark Structured Streaming** job (not legacy DStreams) — Structured Streaming is required
for first-class **event-time** windowing and watermarks, which DStreams lack.

- **Windowing.** A **sliding window**: **5-minute window length, 1-minute slide**, over **event time**. Each
  ping contributes to every overlapping window it falls in (so a given event-time instant is counted across
  several overlapping 5-minute windows emitted 1 minute apart). `window_end − window_start` on the wire is
  therefore always exactly the 5-minute window length.
- **Watermark.** An event-time **watermark of 2 minutes**: a ping whose event time lags the current watermark
  is **dropped** (too late to fold into its window) and counted in a producer-side **`dropped_late`** operational
  metric. `dropped_late` is a producer metric **only** — it is **NOT** a message field and never appears on the
  topic.
- **Dual trigger, one job.** The same job runs two ways: `Trigger.ProcessingTime` drives the **live** heatmap
  (continuous micro-batches), and `Trigger.AvailableNow` drives the **batch** pass over the full ~15M-ping file
  for the "Spark batch processing" headline number. **Same code, two run modes** — not two jobs — so both modes
  emit the identical schema and the **same `producer` value**.
- **Producer emission order (SHOULD).** The producer SHOULD append per-`segment_id` records in
  **non-decreasing `(window_start, emit_time)` order** (newest window last). Correctness does **not** depend on
  it — every consumer applies the dedup rule in-memory (see "Dedup rule" and "Kafka topics") — but
  log-compaction fidelity does: an out-of-order append can leave the compacted log holding a value-older
  record, which a cold consumer would warm-start from before its in-memory dedup corrects it. This obligation
  is restated for producers here because it is easy to miss in the consumer-facing "Kafka topics" section.

### Unit contract (the other half of §2)

`vehicle_count` is a **per-5-minute count**, and BPR needs flow `v` in **vehicles per hour** (§2's unit
contract: `v` and `c = capacity_vph` MUST both be vph, and with `β = 4` a unit mismatch is raised to the
fourth power and silently corrupts the headline number). The engine's congestion adapter therefore scales
each window's count to an hourly rate as:

```
v_vph = vehicle_count × 12          (twelve 5-minute windows per hour)
```

The `× 12` scaling is the **engine adapter's responsibility** (consumer side); it is documented here because it
is the half of the §2 unit contract that lives on this topic. `capacity_vph` (§2) is already vehicles/hour, so
once the adapter applies `× 12`, `v` and `c` share a unit and `v/c` is dimensionless. A producer MUST emit the
raw per-window count and MUST NOT pre-multiply by 12.

### Kafka topics

Two topics frame this pipeline; this contract governs the second, but both are pinned here because their keying
and retention are load-bearing for the dedup rule and the batch replay.

- **`gps-pings`** (input, raw pings — not this contract's payload, listed for context). Keyed by **`taxi_id`**.
  Keying by `taxi_id` preserves per-trajectory ping ordering within a partition — keying by anything else
  shatters a vehicle's trajectory across partitions and breaks map-matching. **12–24 partitions.** Retention is
  set **≥ a full replay window** so the `Trigger.AvailableNow` batch run can re-read the entire ping file.
- **`segment-congestion`** (output, **this contract**). Keyed by **`segment_id`** (the message key above), so
  all records for one segment land on one partition and compaction can collapse them. **`cleanup.policy=compact`.**
  Compaction **approximates, but is not identical to, the dedup rule** and the difference matters. Kafka log
  compaction retains the latest record per key **by append (offset) order** — i.e. by produce order — *not* by
  the contract's `(window_start, emit_time)` value order. The two coincide **only if the producer appends
  records for a given `segment_id` in non-decreasing `(window_start, emit_time)` order** (newest window last);
  under that precondition compaction leaves the keep-latest winner as the surviving value, and a fresh consumer
  of the compacted log converges to the same per-segment state an online consumer reaches by applying the dedup
  rule. That precondition is **not guaranteed**: late-data revisions, producer retries, and the
  `AvailableNow` batch pass interleaving with the live stream can all append a value-**older** record after a
  value-newer one, in which case compaction would retain the wrong (append-latest but value-older) record.
  **Therefore the `(window_start, emit_time)` dedup rule is authoritative and every consumer MUST apply it
  in-memory; a consumer MUST NOT treat Kafka compaction order as a substitute for it.** Compaction is a
  storage-bound optimization (it keeps the log small and lets a cold consumer warm-start near the right state),
  not the definition of correctness.

### Reference / fixtures

- **Golden fixture (language-neutral, shared):** `docs/fixtures/segment_congestion/example_messages.json` — an
  array of messages with all 10 fields, where every `segment_id` is valid under §1, several reuse `segment_id`s
  from the §2 `edge_attributes` fixture (so the engine can join congestion onto known edges), every window is
  exactly 5 minutes, and the rows are constructed to exercise the dedup `(window_start, emit_time)` ordering and
  directionality. Each row also carries a non-contract `note` field describing what it exercises; it is
  documentation only and is **not** part of the schema. See that directory's `README.md`.
- The `segment_id` rules and their own fixtures live in §1 / `docs/fixtures/segment_id/`; this section never
  redefines them.

### Versioning

This is **Contract version: 1**. The field set, their order and types, the half-open 5-minute window semantics,
the dedup `(window_start, emit_time)` rule, the streaming/windowing/watermark semantics, the unit contract, and
the topic keying/compaction rules are frozen. **Changing any of them — adding/removing a field, changing the
window length, the watermark, the dedup ordering, the message key, or the compaction policy — requires bumping
the contract version here (and therefore the on-wire `schema_version`) and notifying every consumer (engine
adapter + static replayer, frontend) so they update in lockstep.** The fixture in
`docs/fixtures/segment_congestion/` is part of this contract; do not edit it to paper over a non-conformant
producer.

As §1 notes, `segment_id` carries no in-wire version token, so **this message's `schema_version` is one of
`segment_id`'s operational version-carriers**: a §1 format change REQUIRES bumping this contract version (and
therefore the `schema_version` on every message) too, so a consumer reading the envelope can detect the change
at ingest rather than silently mis-parsing the message key.
