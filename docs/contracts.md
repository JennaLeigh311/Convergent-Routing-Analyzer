# Frozen Cross-Team Contracts

> The three contracts the board froze at kickoff (`project-spec.md §R0`). This file is the single source
> of truth shared by the database, data-pipeline, and routing-engine teams. **Do not change a frozen
> contract without bumping its version and notifying all consumers.**

## 1. Canonical `segment_id` — *to be filled by issue #3*

`segment_id = "{osm_way_id}:{seq}:{dir}"` (directed). Golden fixtures pending.

## 2. `edge_attributes` export schema — *to be filled by issue #4*

One row per directed edge; carries the BPR capacity/free-flow fields derived from OSM tags.

## 3. `segment-congestion` schema v2 — *to be filled by issue #5*

Event-time windowed congestion records on a compacted Kafka topic keyed by `segment_id`.
