// Package api holds the routing-server's REST handlers over an immutable,
// already-loaded road graph (issue #92, project-spec.md §R6/§R2).
//
// The package is a thin COMPOSITION layer: it wires the engine pieces — the
// GeoJSON loader's graph + geometry, the BPR cost function, the six routers, and
// the simulator congestion adapter (via the shared internal/congestion/source
// seam) — behind HTTP handlers. It adds NO algorithm code; every route decision
// is delegated to internal/routing and every benchmark to internal/benchmark.
//
// The surface (all over the toy edge_attributes graph for now):
//
//	GET  /route       single A→B route: segment_id list + routing cost.
//	GET  /compare     naive vs a congestion-aware router on the SAME OD pair.
//	GET  /congestion  current per-segment congestion snapshot.
//	GET  /graph       the network geometry as GeoJSON, keyed by segment_id — the
//	                  frontend's geometry source. Coloring is a pure segment_id
//	                  join on the client (§R2): /graph carries NO congestion.
//	POST /benchmark   start an async #91 sweep; returns a job id immediately and
//	                  NEVER blocks the request on a systemoptimal run.
//	GET  /benchmark/{id}  poll a job's status/result.
//
// A Server is built once from a loaded graph and is safe for concurrent use: the
// graph and geometry are immutable after load, the congestion provider is read
// only on the request path, and the benchmark job store guards its own map. The
// thin cmd/routing-server binary owns the listener, timeouts, and lifecycle and
// mounts Server.Routes onto its mux alongside the existing /healthz, /readyz and
// /metrics endpoints.
//
// The WebSocket congestion stream (snapshot + delta protocol) is deliberately
// NOT here: it lands in #93 and needs the WriteTimeout caveat the binary
// documents addressed first, so this package stays request/response only.
package api
