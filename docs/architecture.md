# Architecture

> Stub. Expanded from `project-spec.md §2`. Covers the ports-and-adapters design, the data/service/
> presentation planes, the four core ports, and per-profile resource footprints (issue #6).

## Conventions (set in Phase 0)

**Go module rooted at `engine/`, not the repo root.** This is a polyglot repo (Go engine, PySpark
pipeline, React frontend). Rooting the module at `engine/` keeps `go build ./...` from scanning
`pipeline/`, `web/`, and `data/`, and gives each language ecosystem its own root. A future second Go module
or a `go.work` workspace can be added without disturbing this one. Do not "fix" the module to the repo root.

**Shared identifiers live in `internal/domain`.** `EdgeID`, `NodeID`, `SegmentID`, `LatLon`, and
`Direction` live in the `domain` leaf package, which imports nothing from the rest of the engine and may be
imported by all. This keeps `congestion`, `cost`, `api`, etc. from depending on `graph` just to name an ID,
and keeps the dependency direction acyclic.

**Ports at package root, adapters in subpackages.** Each port interface (`Graph`, `CongestionProvider`,
`CostFunction`, `Router`) is defined at its package root. Concrete adapters go in subpackages
(`congestion/memory`, `congestion/static`, `congestion/kafka`, `graph/loader`, …). Import paths — not
reviewer vigilance — keep the engine core free of transport dependencies: the core imports the port
package, never an adapter subpackage that pulls in a Kafka client or PostGIS driver.

**Structured logging via `log/slog`, configured once per binary.** All logging goes through
`internal/logging`, a thin wrapper over the stdlib `log/slog`. Each binary calls `logging.Setup()` at
process start, which builds a logger from `LOG_LEVEL` (`debug|info|warn|error`) and `LOG_FORMAT`
(`text|json`) and installs it as the slog default. Use `text` in development and `json` in deployed
environments for aggregation. Prefer structured key/value fields over formatted strings
(`logger.Info("routed", "requests", n, "algo", name)`, not `fmt.Sprintf`) so logs stay queryable; never
use bare `fmt.Println` for diagnostics. Entrypoints (`cmd/*`) hold the explicit `*slog.Logger` returned by
`Setup`; library and leaf packages take an injected `*slog.Logger` or use `slog.Default()`, never
constructing their own. **Never log secrets, credentials, full DSNs, or API keys, and treat raw GPS
coordinates as user-location PII** — log only derived values (segment IDs, counts), never raw lat/lon tied
to a device.
