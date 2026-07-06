#!/bin/sh
# run_pipeline.sh — end-to-end Porto edge_attributes pipeline (issue #120, Phase 8).
#
# Orchestrates, on top of the existing `data` compose profile (PostGIS/pgRouting):
#   1. fetch a bounded Porto OSM extract (fetch_porto_osm.sh)          [host curl]
#   2. bring up the `postgis` service and wait until it is healthy     [compose]
#   3. build the cra/data-tools image (osm2pgrouting + python exporter) [docker]
#   4. osm2pgrouting-load the extract into PostGIS                      [data-tools]
#   5. export edge_attributes.geojson from pgRouting + raw tags         [data-tools]
#
# The data-tools client joins the SAME docker network as the postgis service and
# reaches it by service name `postgis:5432`, so there is no host-port/loopback
# dependency. Re-runnable; osm2pgrouting is invoked with --clean.
#
# Env overrides:
#   SKIP_FETCH=1   reuse an existing data/osm/porto.osm
#   PORTO_BBOX     forwarded to fetch_porto_osm.sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
cd "$REPO_ROOT"

COMPOSE="${COMPOSE:-docker compose}"
OSM_OUT="$REPO_ROOT/data/osm/porto.osm"
GEOJSON_OUT="$REPO_ROOT/data/out/edge_attributes.geojson"
TOOLS_IMAGE="cra/data-tools:phase8"

PG_DB="${POSTGRES_DB:-routing}"
PG_USER="${POSTGRES_USER:-routing}"
PG_PASSWORD="${POSTGRES_PASSWORD:-routing}"

mkdir -p "$REPO_ROOT/data/out"

# --- 1. fetch -----------------------------------------------------------------
if [ "${SKIP_FETCH:-0}" = "1" ] && [ -f "$OSM_OUT" ]; then
  echo "run_pipeline: SKIP_FETCH=1, reusing $OSM_OUT" >&2
else
  sh "$SCRIPT_DIR/fetch_porto_osm.sh"
fi

# --- 2. postgis up + healthy --------------------------------------------------
echo "run_pipeline: starting postgis (data profile) ..." >&2
$COMPOSE --profile data up -d postgis

PG_CID=$($COMPOSE --profile data ps -q postgis)
if [ -z "$PG_CID" ]; then
  echo "run_pipeline: ERROR — postgis container not found" >&2
  exit 1
fi

echo "run_pipeline: waiting for postgis to be healthy ..." >&2
i=0
while [ "$i" -lt 60 ]; do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$PG_CID" 2>/dev/null || echo starting)
  [ "$status" = "healthy" ] && break
  i=$((i + 1))
  sleep 2
done
if [ "$status" != "healthy" ]; then
  echo "run_pipeline: ERROR — postgis did not become healthy (status=$status)" >&2
  exit 1
fi

# Resolve the compose network the postgis container is attached to, so the
# ephemeral data-tools client can reach it by service name. Emit one network per
# line and take the first, so a container on more than one network yields a single
# valid name (not the concatenation of all of them).
NET=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}' "$PG_CID" | head -1)
echo "run_pipeline: postgis healthy on network $NET" >&2

# Ensure the routing DB has the extensions osm2pgrouting needs.
docker exec -e PGPASSWORD="$PG_PASSWORD" "$PG_CID" \
  psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 \
  -c "CREATE EXTENSION IF NOT EXISTS postgis; CREATE EXTENSION IF NOT EXISTS pgrouting;" >&2

# --- 3. build the data-tools image -------------------------------------------
echo "run_pipeline: building $TOOLS_IMAGE ..." >&2
docker build -t "$TOOLS_IMAGE" "$SCRIPT_DIR" >&2

# --- 4. osm2pgrouting load ----------------------------------------------------
echo "run_pipeline: loading $OSM_OUT into PostGIS via osm2pgrouting ..." >&2
# DB creds are passed as container env vars and referenced from INSIDE the
# single-quoted body, so no host value is spliced into the script text — a
# password containing a space, $, ` or ; can't word-split or inject here.
docker run --rm --network "$NET" -v "$REPO_ROOT/data:/data" \
  -e PG_DB="$PG_DB" -e PG_USER="$PG_USER" -e PG_PASSWORD="$PG_PASSWORD" \
  "$TOOLS_IMAGE" sh -c '
  set -eu
  CONF=$(ls /usr/share/osm2pgrouting/mapconfig.xml 2>/dev/null \
        || find /usr -name mapconfig.xml 2>/dev/null | head -1)
  echo "osm2pgrouting: using mapconfig $CONF" >&2
  osm2pgrouting \
    --f /data/osm/porto.osm \
    --conf "$CONF" \
    --dbname "$PG_DB" \
    --username "$PG_USER" \
    --password "$PG_PASSWORD" \
    --host postgis \
    --port 5432 \
    --clean
' >&2

# --- 5. export edge_attributes.geojson ---------------------------------------
echo "run_pipeline: exporting edge_attributes.geojson ..." >&2
docker run --rm --network "$NET" -v "$REPO_ROOT/data:/data" "$TOOLS_IMAGE" \
  python3 /data/scripts/export_edge_attributes.py \
    --osm /data/osm/porto.osm \
    --out /data/out/edge_attributes.geojson \
    --pg-host postgis --pg-port 5432 \
    --pg-db "$PG_DB" --pg-user "$PG_USER" --pg-password "$PG_PASSWORD"

echo "run_pipeline: DONE -> $GEOJSON_OUT" >&2
