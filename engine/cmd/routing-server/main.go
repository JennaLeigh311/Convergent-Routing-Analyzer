// Command routing-server is the REST API in front of the routing engine.
//
// It loads the immutable toy edge_attributes graph ONCE at startup (embedded in
// the binary so the distroless container needs no testdata on disk), composes the
// engine — cost functions, the six routers, the simulator congestion adapter — behind
// the internal/api handlers, and serves the §R6 REST surface:
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

	addr := serveraddr.Resolve()

	// One registry serves both the runtime/process collectors and the routing
	// request counters api.NewDefaultServer registers, so a single /metrics scrape
	// reports both.
	reg := metrics.NewRegistry()

	apiServer, err := api.NewDefaultServer(reg, logger)
	if err != nil {
		// A graph that won't load is fatal: the server has nothing to route over,
		// so exit non-zero rather than serve a broken surface.
		logger.Error("load routing engine failed", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: newMux(reg, apiServer),
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

// newMux builds the routing-server's full HTTP route table: the api.Server's REST
// handlers plus the health and observability endpoints. Extracted from main so the
// route table — the server's public contract — is assertable in tests without
// binding a real listener (see main_test.go). The api routes are mounted by
// copying each pattern onto the top-level mux so /metrics, /healthz and /readyz
// (owned here, not by the api package) coexist with them.
func newMux(reg *prometheus.Registry, apiServer *api.Server) *http.ServeMux {
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
	apiMux := apiServer.Routes()
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
