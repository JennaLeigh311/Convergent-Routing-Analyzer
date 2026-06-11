// Command benchmark is the Phase-1 routing smoke/timing harness. It is NOT the
// full convergent-routing comparison across the six algorithms — that harness
// (demand-aware strategies, realized-time accounting, the headline improvement
// number) lands in a later phase. What this does today is honest and minimal:
// load the toy edge_attributes GeoJSON, build the naive (free-flow) router, run
// a small batch of real routing requests over the toy graph, time it, and print
// a short factual summary (nodes, edges, requests routed, elapsed).
//
// Its job is to make CI Lane A genuinely gate routing: any load error or routing
// error exits non-zero, so a broken loader/graph/router fails the build rather
// than slipping through unit tests alone. Diagnostics go through the internal
// slog logger (to stderr), consistent with the rest of the engine.
package main

import (
	"context"
	"os"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/logging"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// toyNetworkPath is the shared toy edge_attributes fixture, resolved relative to
// engine/ (where `go run ./cmd/benchmark` and `make bench` run from).
const toyNetworkPath = "testdata/toy_network.geojson"

func main() {
	logger := logging.Setup()

	// Load the toy network. A failure here is fatal: the bench cannot route
	// without a graph, and CI must see a non-zero exit.
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile(toyNetworkPath)
	if err != nil {
		logger.Error("load toy network failed", "component", "benchmark", "path", toyNetworkPath, "err", err)
		os.Exit(1)
	}

	// Build the naive free-flow router and route a small representative batch over
	// the toy graph. The coordinates match the toy fixture's documented endpoints
	// (the toy network is largely one-way): bench-0 is origin node 0 -> destination
	// node 2, exercising the lowest-cost (≠ fewest-hops) path the network was built
	// to demonstrate; bench-1 is node 3 -> node 4 over the two-way secondary pair.
	router := routing.NewNaiveRouter(g)
	reqs := []routing.RouteRequest{
		{ID: "bench-0", From: domain.LatLon{Lat: 40.73, Lon: -73.99}, To: domain.LatLon{Lat: 40.74, Lon: -73.97}},
		{ID: "bench-1", From: domain.LatLon{Lat: 40.742, Lon: -73.965}, To: domain.LatLon{Lat: 40.745, Lon: -73.96}},
	}

	start := time.Now()
	routes, err := router.Assign(context.Background(), reqs)
	elapsed := time.Since(start)
	if err != nil {
		logger.Error("routing failed", "component", "benchmark", "err", err)
		os.Exit(1)
	}

	// Honest summary: this is a Phase-1 naive-router run over the toy graph, not
	// the six-algorithm comparison. Report only what actually happened.
	logger.Info("phase-1 naive-router toy-graph bench",
		"component", "benchmark",
		"router", router.Name(),
		"nodes", g.NodeCount(),
		"edges", g.EdgeCount(),
		"requests_routed", len(routes),
		"elapsed", elapsed.String(),
	)
}
