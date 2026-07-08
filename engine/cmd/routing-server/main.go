// Command routing-server is the REST API in front of the routing engine.
//
// It loads an immutable edge_attributes graph ONCE at startup — by default the toy
// network EMBEDDED in the binary (so the distroless container needs no testdata on disk),
// or, when -graph-file (env GRAPH_FILE) points at one, the real edge_attributes GeoJSON
// export from disk (e.g. Porto's data/out/edge_attributes.geojson, issue #121; never
// embedded). It then composes the engine — cost functions, the six routers, the simulator
// congestion adapter — behind the internal/api handlers, and serves the §R6 REST surface:
//
//	GET  /route       single A→B route (segment_id list + cost)
//	GET  /compare     naive vs congestion-aware on the same OD pair
//	GET  /congestion  current per-segment congestion snapshot
//	GET  /graph       network geometry as GeoJSON (segment_id-keyed; the §R2 join source)
//	POST /benchmark   start an async #91 sweep; returns a job id immediately
//	GET  /benchmark/{id}  poll a benchmark job's status/result
//	GET  /stream      WebSocket: six algorithms simulated in parallel, snapshot + bucketed deltas (#93)
//
// alongside the liveness/readiness/observability endpoints §R7/§R0 mandate:
//
//	GET  /healthz     liveness
//	GET  /readyz      readiness (graph loaded)
//	GET  /metrics     Prometheus scrape (runtime series + routing request counters)
//
// This binary stays THIN: the handler logic lives in internal/api so it is
// testable without binding a listener (mirroring how cmd/route keeps logic in a
// run function). main owns only the mux wiring and the process lifecycle —
// graph load, signal handling, graceful shutdown — and the server timeouts.
//
// The WebSocket congestion stream (/stream, #93) is a LONG-LIVED connection and so
// does NOT want the http.Server's WriteTimeout (it would kill a healthy stream). The
// coder/websocket library handles this by hijacking the connection on Accept and
// managing its own per-write deadlines (internal/api enforces a short deadline per
// frame), so the connection-level WriteTimeout no longer applies once the upgrade
// completes — the server's request/response timeouts stay as-is and the stream is
// unaffected. See internal/api/stream.go (writeFrame) for the per-frame deadline.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/api"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/logging"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/metrics"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/serveraddr"
)

func main() {
	logger := logging.Setup()
	logger = logger.With("service", "routing-server")

	// -graph-file (or the GRAPH_FILE env, its default) points the server at a real
	// edge_attributes GeoJSON on disk (e.g. the Porto export at
	// data/out/edge_attributes.geojson, issue #121). Left empty — the CI/container
	// default — the server boots the toy network EMBEDDED in the binary, so the
	// distroless image needs no testdata on disk. Set it and the ~1.7 MB real export is
	// loaded from the filesystem instead; it is never embedded. The env-backed default
	// makes it settable in a container without editing the command line.
	// Parse into a LOCAL FlagSet rather than the global flag.CommandLine, mirroring
	// cmd/route/main.go — so the binary's flags stay isolated from any ambient global state
	// (and remain testable via a run function later). flag.ExitOnError exits non-zero on a bad
	// flag, matching the previous flag.Parse() behavior.
	fs := flag.NewFlagSet("routing-server", flag.ExitOnError)
	graphFile := fs.String("graph-file", os.Getenv("GRAPH_FILE"),
		"path to a real edge_attributes GeoJSON to serve; empty = embedded toy network (default)")
	_ = fs.Parse(os.Args[1:])

	addr := serveraddr.Resolve()

	// One registry serves both the runtime/process collectors and the routing
	// request counters the api server registers, so a single /metrics scrape
	// reports both.
	reg := metrics.NewRegistry()

	// Choose the graph source: the embedded toy network by default (no filesystem
	// dependency, the CI/container path), or the real on-disk export when -graph-file is
	// set (issue #121). Both build the identical Server surface behind the same §2
	// contract loader.
	apiServer, err := loadServer(*graphFile, reg, logger)
	if err != nil {
		// A graph that won't load is fatal: the server has nothing to route over,
		// so exit non-zero rather than serve a broken surface.
		logger.Error("load routing engine failed", "err", err, "graph_file", *graphFile)
		os.Exit(1)
	}
	if *graphFile != "" {
		logger.Info("serving real network from file", "graph_file", *graphFile)
	} else {
		logger.Info("serving embedded toy network")
	}

	// Resolve the auth + rate-limit policy from the environment once, and build the
	// security wrapper here so main owns the env→config read and newMux just applies
	// it around the api mux (health/metrics stay outside it).
	secure := api.NewSecurityMiddleware(api.LoadSecurityConfig(), logger)

	srv := &http.Server{
		Addr:    addr,
		Handler: newMux(reg, apiServer, secure),
		// Bound every phase of a request so a slow or idle client can't pin a
		// connection on the published port. ReadHeaderTimeout predates the other
		// three; the rest landed alongside /metrics. /benchmark sidesteps the
		// WriteTimeout by returning a job id immediately and running the sweep async,
		// so no handler blocks past it waiting on a systemoptimal run. /stream (#93)
		// also sidesteps it: coder/websocket hijacks the connection on Accept and
		// manages its own per-write deadlines, so the connection-level WriteTimeout no
		// longer applies to the long-lived stream once the upgrade completes.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Shut down cleanly on SIGINT/SIGTERM so `docker compose down` is graceful.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Surface a fatal serve error (e.g. the address is already bound) so the
	// process exits non-zero rather than masquerading as a clean shutdown.
	srvErr := make(chan error, 1)
	go func() {
		logger.Info("routing server listening", "addr", addr,
			"endpoints", "/route,/compare,/congestion,/graph,/benchmark,/stream,/healthz,/readyz,/metrics")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-srvErr:
		logger.Error("routing server failed", "err", err)
		os.Exit(1)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
}

// loadServer builds the api.Server over the chosen graph source: the embedded toy
// network when graphFile is empty (the CI/container default — no filesystem dependency),
// or the real edge_attributes export at graphFile when set (issue #121). It is the single
// graph-source seam so main's lifecycle code stays agnostic to which network is served,
// and it is extracted from main so the selection is unit-testable without binding a
// listener (see main_test.go). Both branches enforce the same §2 contract and return a
// clean error rather than a half-built server.
func loadServer(graphFile string, reg *prometheus.Registry, logger *slog.Logger) (*api.Server, error) {
	if graphFile != "" {
		return api.NewFileServer(graphFile, reg, logger)
	}
	return api.NewDefaultServer(reg, logger)
}

// newMux builds the routing-server's full HTTP route table: the api.Server's REST
// handlers plus the health and observability endpoints. Extracted from main so the
// route table — the server's public contract — is assertable in tests without
// binding a real listener (see main_test.go). The api routes are mounted by
// copying each pattern onto the top-level mux so /metrics, /healthz and /readyz
// (owned here, not by the api package) coexist with them.
//
// secure is the auth + per-client rate-limit wrapper. It is applied around the
// api mux ONLY: /healthz, /readyz and /metrics are mounted before it so liveness,
// readiness, and Prometheus scrapes are never gated by auth or throttling.
func newMux(reg *prometheus.Registry, apiServer *api.Server, secure func(http.Handler) http.Handler) *http.ServeMux {
	mux := http.NewServeMux()

	// /healthz — liveness: the process is up. /readyz — readiness: the graph
	// loaded (NewDefaultServer succeeded before this mux is built), so the server
	// is ready to route. Both return 200 once the server is constructed.
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", healthHandler)

	// /metrics — Prometheus scrape (§R0), serving the shared registry that carries
	// both the runtime collectors and the routing request counters.
	mux.Handle("/metrics", metrics.HandlerFor(reg))

	// The REST surface (/route, /compare, /congestion, /graph, /benchmark[/{id}]).
	// Build the api mux once and delegate each top-level pattern to it so the
	// health/metrics endpoints (owned here) and the api endpoints share one server
	// mux without the api package re-declaring patterns it doesn't own.
	apiMux := secure(apiServer.Routes())
	for _, pattern := range []string{"/route", "/compare", "/congestion", "/graph", "/benchmark", "/benchmark/", "/stream"} {
		mux.Handle(pattern, apiMux)
	}

	return mux
}

// healthHandler reports liveness/readiness: always 200 "ok" once the server is up
// (the graph already loaded successfully, so there is no further readiness gate).
func healthHandler(responseWriter http.ResponseWriter, _ *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = responseWriter.Write([]byte("ok"))
}
