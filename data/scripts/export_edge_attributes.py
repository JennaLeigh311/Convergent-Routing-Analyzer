#!/usr/bin/env python3
"""export_edge_attributes.py — emit a contract-conformant edge_attributes.geojson.

Phase 8, issue #120. This is the PRODUCER half of the frozen edge_attributes
export contract (docs/contracts.md §2, GeoJSON serialization) and the canonical
segment_id wire contract (§1). It reads the topology osm2pgrouting built in
PostGIS/pgRouting (the `ways` table: one row per way sub-segment split at
intersections) and the raw OSM tags from the source .osm extract, then writes one
GeoJSON Feature per DIRECTED edge with all 11 §2 property columns + the derived
capacity / free-flow / lanes fields.

Division of labour:
  * pgRouting provides TOPOLOGY: osm_id, the source/target vertex ids, the split
    geometry (one way -> ordered sub-segments), and vertex coordinates.
  * this script derives every §2 field from the RAW OSM tags exactly per the §2
    derivation tables (capacity, freeflow, class_factor, lanes_effective,
    highway_class, maxspeed) — it does NOT trust osm2pgrouting's own speed/lane
    defaulting, which uses a different (mapconfig) default table.

segment_id = "{osm_way_id}:{seq}:{dir}" (§1). seq is the 0-based ordinal of the
sub-segment among its way's EMITTED sub-segments (gid order); dir is F (along the
way's node ordering) or R (against it). A two-way way emits F and R; a one-way way
emits only its permitted direction.

length_m is computed as the sum of haversine distances over the EMITTED (rounded)
coordinates, using the identical earth radius the Go loader uses
(engine/internal/graph/geo.go, 6_371_000 m). Because a polyline of great-circle
arcs is never shorter than the direct arc between its ends (triangle inequality on
the sphere), length_m >= the endpoint chord the loader checks — the §2 invariant.
Multi-point edges clear the chord with wide margin; a straight 2-point edge matches
the chord to within a few ULPs (Python and Go group the haversine terms slightly
differently), comfortably inside the loader's 1e-9 relative tolerance.

psycopg2 is imported lazily inside main(), not at module top, so the pure §2
derivation logic here (build_features + the helpers) can be imported and
unit-tested without the DB driver installed — see test_export.py.
"""

import argparse
import json
import math
import sys
import xml.etree.ElementTree as ET

# --- §2 derivation tables (frozen — reproduce EXACTLY) -----------------------

HIGHWAY_CLASSES = (
    "motorway", "trunk", "primary", "secondary", "tertiary", "residential", "service",
)

LANES_DEFAULT = {
    "motorway": 3, "trunk": 2, "primary": 2, "secondary": 2,
    "tertiary": 1, "residential": 1, "service": 1,
}
SPEED_DEFAULT = {
    "motorway": 100.0, "trunk": 80.0, "primary": 60.0, "secondary": 50.0,
    "tertiary": 40.0, "residential": 30.0, "service": 20.0,
}
CLASS_FACTOR = {
    "motorway": 1.0, "trunk": 0.9, "primary": 0.8, "secondary": 0.7,
    "tertiary": 0.6, "residential": 0.5, "service": 0.4,
}
SATURATION_FLOW = 1800.0  # veh/h/lane
CAPACITY_SCALE = 1.0      # §2: the export is generated at scale 1.0

# Coordinate output precision. 7 decimal places ~= 1 cm, matching the loader's
# node-position epsilon (nodePosEpsilonDeg = 1e-7 deg). Two edges that share a
# pgRouting vertex carry that vertex's identical coordinate, so they round to the
# same 7-dp value and the loader's re-used-node reconciliation passes.
COORD_DP = 7

EARTH_RADIUS_M = 6_371_000.0  # identical to engine/internal/graph/geo.go


def haversine_m(lon1, lat1, lon2, lat2):
    """Great-circle distance in meters — identical formula/radius to the Go loader."""
    rlat1, rlat2 = math.radians(lat1), math.radians(lat2)
    dlat = math.radians(lat2 - lat1)
    dlon = math.radians(lon2 - lon1)
    a = math.sin(dlat / 2) ** 2 + math.cos(rlat1) * math.cos(rlat2) * math.sin(dlon / 2) ** 2
    return EARTH_RADIUS_M * 2 * math.atan2(math.sqrt(a), math.sqrt(1 - a))


def polyline_len_m(coords):
    """Sum of haversine arcs over the (rounded) coordinate list -> geodesic length."""
    total = 0.0
    for (lon_a, lat_a), (lon_b, lat_b) in zip(coords, coords[1:]):
        total += haversine_m(lon_a, lat_a, lon_b, lat_b)
    return total


# --- raw OSM tag parsing -----------------------------------------------------

def parse_osm_tags(osm_path):
    """way osm_id -> {tag_key: tag_value} for every <way> in the extract."""
    tags_by_way = {}
    for _event, elem in ET.iterparse(osm_path, events=("end",)):
        if elem.tag != "way":
            continue
        wid = int(elem.get("id"))
        tags = {t.get("k"): t.get("v") for t in elem.findall("tag")}
        tags_by_way[wid] = tags
        elem.clear()
    return tags_by_way


def base_highway_class(value):
    """Collapse an OSM highway value to one of the seven §2 classes, or None."""
    if not value:
        return None
    v = value.strip()
    if v.endswith("_link"):
        v = v[:-len("_link")]
    return v if v in LANES_DEFAULT else None


def parse_int(text):
    """Parse a positive int from a possibly-messy OSM lanes value, else None."""
    if text is None:
        return None
    # OSM lanes can be "2", or occasionally "2;3" / "1.5" — take the leading int.
    head = ""
    for ch in text.strip():
        if ch.isdigit():
            head += ch
        else:
            break
    if not head:
        return None
    n = int(head)
    return n if n >= 1 else None


def parse_maxspeed_kmh(text):
    """Parse an OSM maxspeed tag to km/h, else None (fall back to class default)."""
    if text is None:
        return None
    t = text.strip().lower()
    if t in ("none", "signals", "variable", "walk"):
        return None
    num = ""
    for ch in t:
        if ch.isdigit() or ch == ".":
            num += ch
        else:
            break
    if not num:
        return None  # non-numeric implicit zones like "ro:urban" -> class default
    try:
        val = float(num)
    except ValueError:
        return None
    if "mph" in t:
        val *= 1.609344
    return val if val > 0 else None


def directions(tags, hclass):
    """(forward_ok, backward_ok) from the oneway/junction tags + OSM defaults."""
    ow = (tags.get("oneway") or "").strip().lower()
    if ow in ("yes", "true", "1"):
        return True, False
    if ow in ("-1", "reverse"):
        return False, True
    if ow in ("no", "false", "0"):
        return True, True
    # Unspecified: motorways and roundabouts are one-way by OSM convention.
    if hclass == "motorway" or (tags.get("junction") or "").lower() in ("roundabout", "circular"):
        return True, False
    return True, True


def lanes_effective(tags, direction, is_oneway, hclass):
    """§2 lanes_effective precedence: dir-specific tag > bare(one-way) > bare/2 > default."""
    dir_key = "lanes:forward" if direction == "F" else "lanes:backward"
    dir_lanes = parse_int(tags.get(dir_key))
    if dir_lanes is not None:
        return dir_lanes
    bare = parse_int(tags.get("lanes"))
    if bare is not None:
        if is_oneway:
            return bare
        return max(1, bare // 2)
    return LANES_DEFAULT[hclass]


def maxspeed_kmh(tags, hclass):
    """§2 maxspeed: bare OSM `maxspeed` if tagged, else the class default.

    §2's maxspeed rule is exactly two branches (tagged `maxspeed`, else the class
    default) — unlike `lanes_effective` it has NO direction-specific tier, so we
    deliberately do NOT consult `maxspeed:forward`/`maxspeed:backward`. maxspeed is
    therefore direction-independent: the F and R rows of a two-way street share it.
    """
    ms = parse_maxspeed_kmh(tags.get("maxspeed"))
    if ms is None:
        ms = SPEED_DEFAULT[hclass]
    return ms


# --- pgRouting topology ------------------------------------------------------

def fetch_ways(conn):
    """Ordered ways rows: (osm_id, source, target, [[lon,lat],...]) grouped by way."""
    with conn.cursor() as cur:
        cur.execute(
            """
            SELECT osm_id, source, target, ST_AsGeoJSON(the_geom) AS geom
            FROM ways
            WHERE the_geom IS NOT NULL AND source IS NOT NULL AND target IS NOT NULL
            ORDER BY osm_id, gid
            """
        )
        rows = cur.fetchall()
    out = []
    for osm_id, source, target, geom_json in rows:
        geom = json.loads(geom_json)
        if geom.get("type") != "LineString":
            continue
        coords = [[round(float(lon), COORD_DP), round(float(lat), COORD_DP)]
                  for lon, lat in geom["coordinates"]]
        out.append((int(osm_id), int(source), int(target), coords))
    return out


# --- main --------------------------------------------------------------------

def build_features(ways_rows, tags_by_way):
    """Turn ordered pgRouting sub-segments + raw tags into directed §2 features."""
    features = []
    edge_id = 0
    seq_counter = {}          # osm_id -> next seq
    stats = {"skipped_class": 0, "skipped_degenerate": 0, "class_hist": {}}

    for osm_id, source, target, coords in ways_rows:
        tags = tags_by_way.get(osm_id, {})
        hclass = base_highway_class(tags.get("highway"))
        if hclass is None:
            stats["skipped_class"] += 1
            continue

        # Degenerate geometry can't form a valid edge (§2 length_m > 0, >= 2 coords).
        if len(coords) < 2 or source == target:
            stats["skipped_degenerate"] += 1
            continue
        length_fwd = polyline_len_m(coords)
        if length_fwd <= 0:
            stats["skipped_degenerate"] += 1
            continue

        # Assign seq only to sub-segments we actually EMIT, so seq stays a
        # contiguous 0-based ordinal among a way's emitted sub-segments (§1). A
        # skipped self-loop/degenerate sub-segment must NOT punch a hole in the
        # numbering, or another system re-deriving segment_id would disagree.
        seq = seq_counter.get(osm_id, 0)
        seq_counter[osm_id] = seq + 1

        fwd_ok, bwd_ok = directions(tags, hclass)
        is_oneway = not (fwd_ok and bwd_ok)
        cf = CLASS_FACTOR[hclass]
        # maxspeed is direction-independent under §2 (no forward/backward tier),
        # so resolve it once for both the F and R rows.
        speed = maxspeed_kmh(tags, hclass)

        emit = []
        if fwd_ok:
            emit.append(("F", source, target, coords, length_fwd))
        if bwd_ok:
            # Haversine is symmetric: the reversed polyline has the identical
            # geodesic length, so reuse length_fwd (no re-summation, and no
            # few-ULP F/R length drift).
            emit.append(("R", target, source, list(reversed(coords)), length_fwd))

        for direction, src, dst, geom_coords, length_m in emit:
            lanes = lanes_effective(tags, direction, is_oneway, hclass)
            capacity = lanes * SATURATION_FLOW * cf * CAPACITY_SCALE
            freeflow = length_m / (speed / 3.6)
            seg = f"{osm_id}:{seq}:{direction}"
            features.append({
                "type": "Feature",
                "geometry": {"type": "LineString", "coordinates": geom_coords},
                "properties": {
                    "segment_id": seg,
                    "edge_id": edge_id,
                    "source_node": src,
                    "target_node": dst,
                    "osm_way_id": osm_id,
                    "highway_class": hclass,
                    "lanes_effective": lanes,
                    # length_m is emitted UNROUNDED on purpose. It is the sum of
                    # haversine arcs over the emitted (rounded) coords, so for a
                    # straight 2-point edge it matches the loader's endpoint chord
                    # to within a few ULPs (inside the loader's 1e-9 tolerance).
                    # Rounding it (even to 6 dp) can push it a fraction of a
                    # micrometer BELOW that chord and trip the §2 length_m >= chord
                    # guard, so we keep full precision — the value stays consistent
                    # with the coordinate precision it is derived from.
                    "length_m": length_m,
                    "maxspeed_kmh": speed,
                    "freeflow_time_s": freeflow,
                    "capacity_vph": round(capacity, 6),
                },
            })
            edge_id += 1
            stats["class_hist"][hclass] = stats["class_hist"].get(hclass, 0) + 1

    return features, stats


def main():
    ap = argparse.ArgumentParser(description="Export edge_attributes.geojson (§2)")
    ap.add_argument("--osm", required=True, help="source .osm extract (for raw tags)")
    ap.add_argument("--out", required=True, help="output edge_attributes.geojson path")
    ap.add_argument("--pg-host", default="postgis")
    ap.add_argument("--pg-port", default="5432")
    ap.add_argument("--pg-db", default="routing")
    ap.add_argument("--pg-user", default="routing")
    ap.add_argument("--pg-password", default="routing")
    args = ap.parse_args()

    print(f"export: parsing raw tags from {args.osm} ...", file=sys.stderr)
    tags_by_way = parse_osm_tags(args.osm)
    print(f"export: {len(tags_by_way)} ways in extract", file=sys.stderr)

    # Lazy import (see module docstring): keeps the derivation logic importable
    # for test_export.py without psycopg2 present.
    import psycopg2

    conn = psycopg2.connect(
        host=args.pg_host, port=args.pg_port, dbname=args.pg_db,
        user=args.pg_user, password=args.pg_password,
    )
    try:
        ways_rows = fetch_ways(conn)
    finally:
        conn.close()
    print(f"export: {len(ways_rows)} pgRouting sub-segments", file=sys.stderr)

    features, stats = build_features(ways_rows, tags_by_way)

    fc = {"type": "FeatureCollection", "schema_version": 1, "features": features}
    with open(args.out, "w") as fh:
        json.dump(fc, fh)

    print(f"export: wrote {args.out}", file=sys.stderr)
    print(f"export: {len(features)} directed edges", file=sys.stderr)
    print(f"export: class histogram {stats['class_hist']}", file=sys.stderr)
    print(f"export: skipped {stats['skipped_class']} (non-vehicle class), "
          f"{stats['skipped_degenerate']} (degenerate geometry)", file=sys.stderr)
    if not features:
        print("export: ERROR — no features produced", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
