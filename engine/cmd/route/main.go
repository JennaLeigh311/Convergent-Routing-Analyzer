// Command route is the thin end-to-end tail of the routing engine: load a toy
// edge_attributes GeoJSON network, snap two lat/lon endpoints to graph nodes,
// and print the naive (free-flow shortest-path) route between them as an ordered
// list of segment_ids plus the summed free-flow cost. It exercises the whole
// Phase-1 stack at once — loader + graph + spatial index + dijkstra + naive
// router — so a single `go run ./cmd/route` from engine/ demonstrates the
// canonical lowest-cost (≠ fewest-hops) route over the toy graph.
//
// The route is the product output, so it is written to STDOUT with fmt; errors
// (bad coordinates, an unloadable graph, an endpoint that snaps to no node, an
// unreachable destination) go to STDERR and exit non-zero. The slog logging
// package is deliberately NOT used for the route line.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// Default flag values — also the canonical Phase-1 demo route over the toy graph:
// node 0 -> node 2, the lowest-cost 2-hop motorway path (NOT the 1-hop residential
// edge). These are the single source of truth for the demo's inputs; main_test.go
// drives the same coordinates, and the route's expected OUTPUT is pinned once in
// testdata/canonical_route.golden (asserted by both the unit test and CI's smoke).
const (
	defaultGraphPath = "testdata/toy_network.geojson"
	defaultFrom      = "40.73,-73.99"
	defaultTo        = "40.74,-73.97"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the whole command so main is a one-line os.Exit wrapper and the
// behavior is testable through in-memory writers. It returns the process exit
// code (0 success, non-zero failure) rather than calling os.Exit itself, and
// uses a local flag.FlagSet (not the global flag.CommandLine) so tests stay
// isolated from one another. All flag/usage output is routed to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	flagSet := flag.NewFlagSet("route", flag.ContinueOnError)
	flagSet.SetOutput(stderr)

	graphPath := flagSet.String("graph", defaultGraphPath,
		"path to an edge_attributes GeoJSON FeatureCollection")
	from := flagSet.String("from", defaultFrom, "origin coordinate as \"lat,lon\" (decimal degrees)")
	toValue := flagSet.String("to", defaultTo, "destination coordinate as \"lat,lon\" (decimal degrees)")

	if err := flagSet.Parse(args); err != nil {
		// flag already printed the error + usage to stderr (our writer).
		return 2
	}

	origin, err := parseLatLon(*from)
	if err != nil {
		fmt.Fprintf(stderr, "route: invalid -from %q: %v\n", *from, err)
		return 1
	}
	dest, err := parseLatLon(*toValue)
	if err != nil {
		fmt.Fprintf(stderr, "route: invalid -to %q: %v\n", *toValue, err)
		return 1
	}

	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(*graphPath)
	if err != nil {
		fmt.Fprintf(stderr, "route: load graph %q: %v\n", *graphPath, err)
		return 1
	}

	router := routing.NewNaiveRouter(roadGraph)
	route, err := router.Route(context.Background(), routing.RouteRequest{
		ID:   "route-cli",
		From: origin,
		To:   dest,
	})
	if err != nil {
		fmt.Fprintf(stderr, "route: %v\n", err)
		return 1
	}

	// Zero-edge result: From and To snapped to the same node. Not an error — a
	// path to where you already are. (See NaiveRouter.Route doc comment.)
	if len(route.Edges) == 0 {
		// Keep the trailing "cost <n> s" line identical to the normal branch so a
		// caller always finds the cost on the last line regardless of path length.
		fmt.Fprintf(stdout, "origin == destination, 0 edges\ncost %.1f s\n", route.CostS)
		return 0
	}

	segments := make([]string, len(route.Edges))
	for index, edgeID := range route.Edges {
		edge, found := roadGraph.Edge(edgeID)
		if !found {
			// The router returned an edge id the graph does not know — an internal
			// inconsistency, not user error. Surface it loudly rather than printing
			// a half-rendered path.
			fmt.Fprintf(stderr, "route: internal error: edge %d not found in graph\n", edgeID)
			return 1
		}
		segments[index] = string(edge.Segment)
	}

	fmt.Fprintf(stdout, "%s\ncost %.1f s\n", strings.Join(segments, " -> "), route.CostS)
	return 0
}

// parseLatLon parses a "lat,lon" pair of decimal degrees into a domain.LatLon.
// It is strict: exactly two comma-separated, parseable float fields, so a
// single value or non-numeric token is rejected with a clear error rather than
// silently snapping to the wrong place.
func parseLatLon(text string) (domain.LatLon, error) {
	parts := strings.Split(text, ",")
	if len(parts) != 2 {
		return domain.LatLon{}, fmt.Errorf("want \"lat,lon\" (two comma-separated decimal degrees), got %d field(s)", len(parts))
	}
	latStr := strings.TrimSpace(parts[0])
	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		return domain.LatLon{}, fmt.Errorf("lat %q is not a number", latStr)
	}
	lonStr := strings.TrimSpace(parts[1])
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		return domain.LatLon{}, fmt.Errorf("lon %q is not a number", lonStr)
	}
	return domain.LatLon{Lat: lat, Lon: lon}, nil
}
