# syntax=docker/dockerfile:1
#
# routing-server image (docker-compose `core` profile, issue #6).
# Multi-stage: build the Go binaries on golang:1.26.4, ship a distroless final
# image running routing-server. Distroless has no shell, so the healthcheck is
# performed by a tiny statically-linked `healthcheck` binary built alongside the
# server (it GETs /healthz on $ROUTING_SERVER_ADDR and exits 0 on HTTP 200).

# ---- build stage ----
FROM golang:1.26.4 AS build
WORKDIR /src

# go.mod first for layer caching. There is no go.sum (no external deps yet);
# the build copies it opportunistically.
COPY go.mod ./
COPY go.su[m] ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 -> a static binary that runs on distroless static.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/routing-server ./cmd/routing-server
RUN go build -trimpath -ldflags="-s -w" -o /out/healthcheck    ./cmd/healthcheck

# ---- final stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /

COPY --from=build /out/routing-server /usr/local/bin/routing-server
COPY --from=build /out/healthcheck    /usr/local/bin/healthcheck

# Documented default; compose overrides via env_file/.env when present.
ENV ROUTING_SERVER_ADDR=:8080
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/routing-server"]
