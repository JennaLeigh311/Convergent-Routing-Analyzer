# `engine/testdata` — shared routing test assets

Module-level shared test fixtures for the routing engine. From a package under
`engine/internal/...` the relative path is `../../testdata/<file>`.

## `toy_network.geojson`

A small, §2-conformant `edge_attributes` GeoJSON `FeatureCollection` (see
`docs/contracts.md` §2) authored for issue #26 as the shared routing test asset
for #27 (the router) and later phases. It loads cleanly through the merged #25
loader (`graph.LoadEdgeAttributesGeoJSONFile`). All values are computed at
`capacity_scale = 1.0` using the §2 derivation rules exactly.

Region: NYC-ish, continuing the golden fixture's window — lon ∈ [-73.99, -73.95],
lat ∈ [40.73, 40.75].

### Topology

6 dense nodes (`0..5`), 7 directed edges (dense `edge_id` `0..6`). Node ids are a
dense `0..NodeCount-1` assignment; node position is the geometry endpoint, shared
node ids carry identical endpoint coordinates across edges (the loader rejects
contradictory shared-node coords beyond 1e-7°).

Node coordinates (`[lon, lat]`):

| node | lon        | lat       |
|------|------------|-----------|
| 0    | -73.99000  | 40.73000  | origin
| 1    | -73.98000  | 40.73500  |
| 2    | -73.97000  | 40.74000  | destination
| 3    | -73.96500  | 40.74200  |
| 4    | -73.96000  | 40.74500  |
| 5    | -73.95500  | 40.74800  |

Edges (one row per directed edge):

| edge_id | segment_id     | from→to | class       | dir   | length_m | maxspeed_kmh | lanes | capacity_vph | freeflow_time_s |
|---------|----------------|---------|-------------|-------|----------|--------------|-------|--------------|-----------------|
| 0       | 9000001:0:F    | 0→2     | residential | 1-way | 900.0    | 30.0         | 1     | 900.0        | 108.0           |
| 1       | 905512:0:F     | 0→1     | motorway    | 1-way | 500.0    | 100.0        | 3     | 5400.0       | 18.0            |
| 2       | 905512:1:F     | 1→2     | motorway    | 1-way | 400.0    | 100.0        | 3     | 5400.0       | 14.4            |
| 3       | 27583001:0:F   | 2→3     | primary     | 1-way | 240.0    | 60.0         | 2     | 2880.0       | 14.4            |
| 4       | 48800123:0:F   | 3→4     | secondary   | F     | 180.0    | 50.0         | 2     | 2520.0       | 12.96           |
| 5       | 48800123:0:R   | 4→3     | secondary   | R     | 180.0    | 50.0         | 2     | 2520.0       | 12.96           |
| 6       | 33112200:0:F   | 4→5     | trunk       | 1-way | 400.0    | 80.0         | 2     | 3240.0       | 18.0            |

Derivation recap (`capacity_vph = lanes × 1800 × class_factor`;
`freeflow_time_s = length_m / (maxspeed_kmh / 3.6)`): class_factor motorway 1.0,
trunk 0.9, primary 0.8, secondary 0.7, residential 0.5.

- **One-way edges:** `9000001:0:F`, `905512:0:F`, `905512:1:F`, `27583001:0:F`,
  `33112200:0:F` (each is a single directed row).
- **Two-way F/R pair:** `48800123:0:F` (edge 4, node 3→4) and `48800123:0:R`
  (edge 5, node 4→3) — same way+seq, `dir` differs, `source_node`/`target_node`
  swapped, and the LineString coordinates reversed (source-first/target-last
  preserved per direction).
- **Interior shape points:** edge 0 (`9000001:0:F`) and edge 6 (`33112200:0:F`)
  each have a 3-coordinate LineString whose middle coordinate is a geometry shape
  point, not a graph node. `NodeCount` counts only the 6 endpoints.

### Intended origin→destination: lowest-cost ≠ fewest-hops

Origin **node 0**, destination **node 2**. There are two routes:

- **Direct, 1 hop:** edge 0 (`9000001:0:F`, residential, 900 m @ 30 km/h).
  Summed `freeflow_time_s` = **108.0 s**.
- **Alternative, 2 hops:** edge 1 (`905512:0:F`) then edge 2 (`905512:1:F`),
  both motorway. Summed `freeflow_time_s` = 18.0 + 14.4 = **32.4 s**.

The 2-hop alternative has **strictly more edges** yet **strictly lower** summed
free-flow cost (32.4 s < 108.0 s). A fewest-hops search returns the direct edge;
a lowest-cost (summed `freeflow_time_s`) search returns the 2-hop motorway path.
This is the acceptance property #27's router relies on, pinned at the data level
by `engine/internal/graph/toynetwork_test.go`.

### Overlap with the segment-congestion fixture

These segment_ids also appear in `docs/fixtures/segment_congestion/example_messages.json`,
so a congestion overlay can be joined onto this network later by `segment_id`
(§1/§3, a pure wire-key join):

- `27583001:0:F` (edge 3)
- `48800123:0:F` (edge 4)
- `48800123:0:R` (edge 5)

## `toy_network_adversarial.geojson`

A second, §2-conformant `edge_attributes` fixture authored for issue #73 to harden
the Phase-3 routing algorithms against real-network pathology the hand-built
`toy_network.geojson` lacks. It loads cleanly through the same `#25` loader and is
exercised by `engine/internal/routing/adversarial_test.go`. Values are computed at
`capacity_scale = 1.0` via the §2 derivation rules. Its `osm_way_id`s are fresh
`71000xx` ids, disjoint from `toy_network.geojson` and the congestion fixture, so
the two networks never collide.

### Topology

6 nodes, 5 directed edges (dense `edge_id` `0..4`), in **two disconnected
components**:

| edge_id | segment_id    | from→to | class       | dir   | length_m | maxspeed_kmh | lanes | capacity_vph | freeflow_time_s |
|---------|---------------|---------|-------------|-------|----------|--------------|-------|--------------|-----------------|
| 0       | 7100001:0:F   | 0→1     | residential | 1-way | 300.0    | 30.0         | 1     | 900.0        | 36.0            |
| 1       | 7100002:0:F   | 1→2     | secondary   | F     | 500.0    | 50.0         | 2     | 2520.0       | 36.0            |
| 2       | 7100002:0:R   | 2→1     | secondary   | R     | 500.0    | 50.0         | 2     | 2520.0       | 36.0            |
| 3       | 7100003:0:F   | 2→3     | primary     | 1-way | 600.0    | 60.0         | 2     | 2880.0       | 36.0            |
| 4       | 7100004:0:F   | 4→5     | tertiary    | 1-way | 400.0    | 40.0         | 1     | 1080.0       | 36.0            |

Node positions (`[lon, lat]`): node 0 `[-73.99,40.73]`, 1 `[-73.98,40.735]`,
2 `[-73.97,40.74]`, 3 `[-73.96,40.745]`, 4 `[-73.90,40.80]`, 5 `[-73.89,40.805]`.

### The two pathologies

- **Disconnected component.** Nodes `4,5` and edge 4 (`7100004:0:F`) form an island
  with **no** edge to or from the main component `{0,1,2,3}`. It sits ~9 km NE of
  the main component so `NearestNode` cannot bridge the gap. An OD pair across the
  gap (e.g. node 0 → node 4) is **genuinely unreachable** — the routing layer must
  return a clean no-route (not a panic/NaN/divide-by-zero).
- **One-way trap.** Edge 3 (`7100003:0:F`, node 2 → node 3) is one-way only: there
  is **no** reverse row (`7100003:0:R` does not exist), so node 3 is a directed sink
  with zero out-edges. The forward OD node 0 → node 3 is reachable (`0→1→2→3`) and
  legitimately uses the trap edge forward; the forbidden direction (node 3 → node 0)
  must return a clean no-route and must **never** walk edge 3 backward. The `7100002`
  F/R pair (node 1 ↔ node 2) is a genuine two-way street, so "no reverse edge" is a
  property of the trap specifically, not of every edge.

### Where unreachability is enforced

The loader does **not** reject disconnected graphs; reachability is a routing-layer
concern. See `docs/architecture.md` → "Graph connectedness: unreachability is a
routing-layer concern, not a loader rejection (issue #73)" for the full decision and
rationale.
