// Command routing-server is the REST + WebSocket API in front of the routing
// engine.
//
// Phase 0 scope (issue #6, sanctioned by the lead): this binary exposes ONLY
// the liveness/readiness endpoints that §R7 mandates on the routing-server plus
// the §R0 /metrics endpoint, so the container can report healthy and be scraped
// under the docker-compose `core` profile. The real routing/graph/API surface
// (the six algorithms, cost functions, the simulator congestion adapter,
// WebSocket snapshots/deltas) lands in a later phase on the routing-engine
// lane — do NOT add that logic here.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/logging"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/serveraddr"
)

func main() {
	logger := logging.Setup()
	logger = logger.With("service", "routing-server")

	addr := serveraddr.Resolve()

	mux := http.NewServeMux()
	// /healthz — liveness: the process is up. /readyz — readiness: the process
	// is ready to serve. In Phase 0 there is no graph or congestion source to
	// wait on, so both return 200 unconditionally. /readyz gains real readiness
	// gating (graph loaded, congestion source connected) in a later phase.
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", healthHandler)
	// /metrics — Prometheus scrape target (§R0 observability deliverable). For
	// now this serves only the default Go runtime/process collectors
	// (go_goroutines, process_*, etc.). Phase 1+ routing registers real request
	// counters/histograms against the default registry, which this handler
	// already exposes.
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Shut down cleanly on SIGINT/SIGTERM so `docker compose down` is graceful.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Surface a fatal serve error (e.g. the address is already bound) so the
	// process exits non-zero rather than masquerading as a clean shutdown.
	srvErr := make(chan error, 1)
	go func() {
		logger.Info("health server listening", "addr", addr,
			"endpoints", "/healthz,/readyz,/metrics")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-srvErr:
		logger.Error("health server failed", "err", err)
		os.Exit(1)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
}

// healthHandler reports liveness/readiness. Phase 0: always 200 "ok".
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
