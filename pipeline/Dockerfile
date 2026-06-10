# syntax=docker/dockerfile:1
#
# pipeline image (docker-compose `full` profile, issue #6).
# Phase-0 placeholder: pipeline/ is currently just a README (the PySpark
# Structured Streaming jobs land in Phases 6-7). Per §R7, Spark runs in
# in-process `local[*]` mode — it is a LIBRARY (pyspark) inside this container,
# NOT a separate Spark master/worker cluster.
#
# This placeholder pins the runtime (python 3.11 + pyspark 3.5.1) so the version
# is frozen now, drops a liveness marker the compose healthcheck can read, and
# idles. The real entrypoint (spark-submit of the streaming job) replaces the
# CMD in Phase 7.
FROM python:3.11-slim-bookworm

# Spark needs a JRE even in local[*] mode.
RUN apt-get update \
 && apt-get install -y --no-install-recommends default-jre-headless procps \
 && rm -rf /var/lib/apt/lists/*

# Pinned PySpark (Spark 3.5.1) — frozen per §R7 / issue #6 version-pinning.
RUN pip install --no-cache-dir pyspark==3.5.1

WORKDIR /pipeline

# Liveness: write a marker on start; the compose healthcheck checks it exists.
# Replaced by spark-submit of the streaming job in Phase 7.
CMD ["sh", "-c", "touch /tmp/pipeline-alive && echo 'pipeline placeholder (PySpark 3.5.1, local[*]); Phase 7 job not yet wired' && tail -f /dev/null"]
