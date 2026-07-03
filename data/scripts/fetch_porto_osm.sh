#!/bin/sh
# fetch_porto_osm.sh — download a small, bounded Porto OSM extract for the
# edge_attributes pipeline (issue #120, Phase 8).
#
# It queries the Overpass API for the drivable highway ways inside a bounding box
# over central Porto (plus every node they reference) and writes an OSM XML file
# osm2pgrouting can load directly. Only the seven §2 highway classes (and their
# _link variants) are fetched, so the extract is a clean vehicle network of a few
# hundred ways — small enough to commit-by-script and demo, never the data blob.
#
# The extract itself is git-ignored (see .gitignore: data/osm/); this SCRIPT is
# the committed, reproducible producer of it. Re-run it to refresh the extract.
#
# Env overrides (defaults = central Porto, ~2.5 x 3 km):
#   PORTO_BBOX   "south,west,north,east" in WGS84 degrees
#   OSM_OUT      output path (default data/osm/porto.osm)
#   OVERPASS_URL Overpass interpreter endpoint
set -eu

# Repo-root-relative paths so the script works from anywhere.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

# south,west,north,east — central Porto (Baixa / Cedofeita / Bonfim).
PORTO_BBOX="${PORTO_BBOX:-41.1400,-8.6300,41.1650,-8.5950}"
OSM_OUT="${OSM_OUT:-$REPO_ROOT/data/osm/porto.osm}"
OVERPASS_URL="${OVERPASS_URL:-https://overpass-api.de/api/interpreter}"

mkdir -p "$(dirname -- "$OSM_OUT")"

# Vehicle classes only: the seven §2 highway_class enum values plus their _link
# ramps. Pedestrian/cycle/foot ways are excluded so the imported network is the
# drivable graph the engine routes on.
QUERY='[out:xml][timeout:180];
(
  way["highway"~"^(motorway|trunk|primary|secondary|tertiary|residential|service)(_link)?$"]('"$PORTO_BBOX"');
);
(._;>;);
out body;'

echo "fetch_porto_osm: bbox=$PORTO_BBOX -> $OSM_OUT" >&2
echo "fetch_porto_osm: querying $OVERPASS_URL ..." >&2

# Overpass asks clients to send a descriptive User-Agent. --fail so an HTTP error
# is a non-zero exit instead of a saved error page.
curl -sS --fail -m 300 \
  -A "traffic-manipulator/1.0 (Phase 8 edge_attributes pipeline; issue #120)" \
  "$OVERPASS_URL" \
  --data-urlencode "data=$QUERY" \
  -o "$OSM_OUT"

WAYS=$(grep -c "<way " "$OSM_OUT" 2>/dev/null || echo 0)
NODES=$(grep -c "<node " "$OSM_OUT" 2>/dev/null || echo 0)
BYTES=$(wc -c < "$OSM_OUT" | tr -d ' ')
echo "fetch_porto_osm: wrote $OSM_OUT ($BYTES bytes, $WAYS ways, $NODES nodes)" >&2

if [ "$WAYS" -eq 0 ]; then
  echo "fetch_porto_osm: ERROR — extract has 0 ways; check the bbox / Overpass status" >&2
  exit 1
fi
