# cra/data-tools — the osm2pgrouting loader + the Python edge_attributes exporter
# for the Phase 8 Porto pipeline (issue #120). It is NOT a long-running service:
# it is an ephemeral client run on the compose `data` network, connecting to the
# `postgis` service to load the OSM extract and export the GeoJSON.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      osm2pgrouting \
      postgresql-client \
      python3 \
      python3-psycopg2 \
      ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /work
