// Command routing-server is the REST + WebSocket API in front of the routing
// engine.
//
// Phase 0 scope (issue #6, sanctioned by the lead): this binary exposes ONLY
// the liveness/readiness endpoints that §R7 mandates on the routing-server, so
// the container can report healthy under the docker-compose `core` profile. The
// real routing/graph/API surface (the six algorithms, cost functions, the
// simulator congestion adapter, WebSocket snapshots/deltas) lands in a later
// phase on the routing-engine lane — do NOT add that logic here.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/logging"
)

// defaultAddr is used when ROUTING_SERVER_ADDR is unset. It matches the
// `.env.example` default so the dev (core) profile needs no configuration.
const defaultAddr = ":8080"

func main() {
	logger := logging.Setup()
	logger = logger.With("service", "routing-server")

	addr := os.Getenv("ROUTING_SERVER_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	mux := http.NewServeMux()
	// /healthz — liveness: the process is up. /readyz — readiness: the process
	// is ready to serve. In Phase 0 there is no graph or congestion source to
	// wait on, so both return 200 unconditionally. /readyz gains real readiness
	// gating (graph loaded, congestion source connected) in a later phase.
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", healthHandler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Shut down cleanly on SIGINT/SIGTERM so `docker compose down` is graceful.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("health server listening", "addr", addr,
			"endpoints", "/healthz,/readyz")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

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
