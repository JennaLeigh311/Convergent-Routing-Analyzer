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

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the whole command so main is a one-line os.Exit wrapper and the
// behavior is testable through in-memory writers. It returns the process exit
// code (0 success, non-zero failure) rather than calling os.Exit itself, and
// uses a local flag.FlagSet (not the global flag.CommandLine) so tests stay
// isolated from one another. All flag/usage output is routed to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.SetOutput(stderr)

	graphPath := fs.String("graph", "testdata/toy_network.geojson",
		"path to an edge_attributes GeoJSON FeatureCollection")
	from := fs.String("from", "40.73,-73.99", "origin coordinate as \"lat,lon\" (decimal degrees)")
	to := fs.String("to", "40.74,-73.97", "destination coordinate as \"lat,lon\" (decimal degrees)")

	if err := fs.Parse(args); err != nil {
		// flag already printed the error + usage to stderr (our writer).
		return 2
	}

	origin, err := parseLatLon(*from)
	if err != nil {
		fmt.Fprintf(stderr, "route: invalid -from %q: %v\n", *from, err)
		return 1
	}
	dest, err := parseLatLon(*to)
	if err != nil {
		fmt.Fprintf(stderr, "route: invalid -to %q: %v\n", *to, err)
		return 1
	}

	g, _, err := graph.LoadEdgeAttributesGeoJSONFile(*graphPath)
	if err != nil {
		fmt.Fprintf(stderr, "route: load graph %q: %v\n", *graphPath, err)
		return 1
	}

	router := routing.NewNaiveRouter(g)
	rt, err := router.Route(context.Background(), routing.RouteRequest{
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
	if len(rt.Edges) == 0 {
		fmt.Fprintf(stdout, "origin == destination, 0 edges, cost %.2f s\n", rt.CostS)
		return 0
	}

	segments := make([]string, len(rt.Edges))
	for i, id := range rt.Edges {
		e, ok := g.Edge(id)
		if !ok {
			// The router returned an edge id the graph does not know — an internal
			// inconsistency, not user error. Surface it loudly rather than printing
			// a half-rendered path.
			fmt.Fprintf(stderr, "route: internal error: edge %d not found in graph\n", id)
			return 1
		}
		segments[i] = string(e.Segment)
	}

	fmt.Fprintf(stdout, "%s\ncost %.1f s\n", strings.Join(segments, " -> "), rt.CostS)
	return 0
}

// parseLatLon parses a "lat,lon" pair of decimal degrees into a domain.LatLon.
// It is strict: exactly two comma-separated, parseable float fields, so a
// single value or non-numeric token is rejected with a clear error rather than
// silently snapping to the wrong place.
func parseLatLon(s string) (domain.LatLon, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return domain.LatLon{}, fmt.Errorf("want \"lat,lon\" (two comma-separated decimal degrees), got %d field(s)", len(parts))
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return domain.LatLon{}, fmt.Errorf("lat %q is not a number", parts[0])
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return domain.LatLon{}, fmt.Errorf("lon %q is not a number", parts[1])
	}
	return domain.LatLon{Lat: lat, Lon: lon}, nil
}
