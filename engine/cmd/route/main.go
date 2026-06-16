// Command route is the thin end-to-end tail of the routing engine: load a toy
// edge_attributes GeoJSON network, snap two lat/lon endpoints to graph nodes,
// and print the chosen route between them as an ordered list of segment_ids plus
// the route cost. It exercises the whole Phase-1 stack at once — loader + graph +
// spatial index + dijkstra + router — so a single `go run ./cmd/route` from
// engine/ demonstrates the canonical lowest-cost (≠ fewest-hops) route over the
// toy graph.
//
// Algorithm and congestion flags (the Phase-2 headline demo, project-spec.md §6):
// the SAME A→B request diverts when congestion is injected. The default --algo is
// naive, which routes free-flow shortest path (the cost is the summed free-flow
// time, unchanged from Phase 1). --algo reactive instead weights edges with the
// BPR cost function (cost.DefaultBPR, project-spec.md §1) over a single FROZEN
// congestion snapshot (the reactive best-response-to-stale-state seam, see
// routing.ReactiveRouter and project-spec.md §4/§R5). The congestion snapshot is
// chosen by:
//
//   - --congestion sim  : a deterministic simulator.New provider (fixed seed,
//     simulatorSeed below) into which --jam injects load.
//   - --congestion PATH : a static.NewProvider built from a §3 segment-congestion
//     JSON batch decoded from PATH.
//   - --congestion (omitted) with --algo reactive: a zero-load memory.New
//     provider, so --jam alone still works and reactive WITHOUT a jam routes the
//     same path as naive (zero load ⇒ BPR collapses to free-flow ordering).
//
// --jam <segment_id> injects a heavy load (--jam-vph, default jamDefaultVPH) onto
// the named segment so reactive avoids it; an unknown segment_id is a clean user
// error. naive ignores all congestion flags (free-flow does not read load).
//
// Documented one-liner that reproduces the divert (project-spec.md §6 demo): from
// engine/,
//
//	go run ./cmd/route --algo reactive --jam 905512:0:F
//
// prints a DIFFERENT path than `go run ./cmd/route --algo naive`: jamming the
// 2-hop motorway corridor (905512:0:F) sends reactive onto the 1-hop residential
// edge 9000001:0:F.
//
// NOTE on the cost line under reactive: a reactive route's "cost <n> s" is the
// summed CONGESTED BPR cost of the chosen edges under the snapshot — the cost the
// path was OPTIMIZED against, not a realized travel time — so it is a different
// (larger) number than naive's free-flow cost for the same path. That is correct,
// not a regression (see routing.ReactiveRouter.Route, project-spec.md §R5).
//
// The route is the product output, so it is written to STDOUT with fmt; errors
// (bad coordinates, an unloadable graph, an endpoint that snaps to no node, an
// unreachable destination, an unknown --jam segment) go to STDERR prefixed
// "route:" and exit non-zero. The slog logging package is deliberately NOT used
// for the route line.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/memory"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/simulator"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/static"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
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

// Algorithm and congestion flag vocabulary. algoNaive is the DEFAULT so the
// no-flags invocation stays the canonical free-flow route; algoReactive opts into
// the BPR-weighted reactive router. congestionSim selects the deterministic
// simulator source (any other --congestion value is treated as a file path).
const (
	algoNaive     = "naive"
	algoReactive  = "reactive"
	congestionSim = "sim"
)

// jamDefaultVPH is the default load --jam injects onto a segment. It is set high
// enough to force the toy-graph divert: jamming the motorway corridor at this
// load lifts its BPR cost above the 1-hop residential edge's free-flow cost, so
// reactive reroutes. Override it with --jam-vph for other graphs.
const jamDefaultVPH = 50000

// simulatorSeed is the FIXED seed for the --congestion sim provider. Pinning it
// makes the simulator-backed demo deterministic (project-spec.md §R5
// reproducibility): the same --jam injection yields the same snapshot, so the
// printed route does not vary run to run.
const simulatorSeed int64 = 1

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
	algo := flagSet.String("algo", algoNaive,
		"routing algorithm: \"naive\" (free-flow shortest path) or \"reactive\" (BPR-weighted over a frozen congestion snapshot)")
	congestionSource := flagSet.String("congestion", "",
		"reactive congestion source: \"sim\" (deterministic simulator), a path to a §3 segment-congestion JSON batch, or empty (zero load)")
	jamSegment := flagSet.String("jam", "",
		"segment_id to inject load onto so reactive avoids it (the Phase-2 divert demo); rejected with a file -congestion, which already carries its own loads")
	jamVPH := flagSet.Float64("jam-vph", jamDefaultVPH,
		"vehicles/hour to inject onto --jam (must be > 0; default is high enough to force the toy-graph divert)")

	if err := flagSet.Parse(args); err != nil {
		// flag already printed the error + usage to stderr (our writer).
		return 2
	}

	if *algo != algoNaive && *algo != algoReactive {
		fmt.Fprintf(stderr, "route: invalid -algo %q: want %q or %q\n", *algo, algoNaive, algoReactive)
		return 1
	}

	// A non-positive --jam-vph is floored to zero by every provider, so the
	// "jammed" edge stays at zero load and reactive never diverts — a request to
	// jam that silently does nothing. Reject it loudly rather than print the
	// un-diverted route. (Only meaningful for reactive + an actual --jam; naive
	// ignores congestion flags, so it is not validated here.)
	if *algo == algoReactive && *jamSegment != "" && *jamVPH <= 0 {
		fmt.Fprintf(stderr, "route: invalid -jam-vph %g: want > 0 (a non-positive load floors to zero and would not divert)\n", *jamVPH)
		return 1
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

	router, err := buildRouter(roadGraph, *algo, *congestionSource, *jamSegment, *jamVPH)
	if err != nil {
		fmt.Fprintf(stderr, "route: %v\n", err)
		return 1
	}

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

// buildRouter selects the routing.Router for the chosen --algo. naive returns the
// free-flow NaiveRouter and ignores every congestion flag (free-flow does not read
// load), so naive + any --congestion/--jam is harmless, not nonsensical. reactive
// returns a ReactiveRouter weighting cost.DefaultBPR over the snapshot built by
// buildCongestionProvider; the caller (run) prefixes any returned error with
// "route:" and exits non-zero. The graph is needed to size the dense per-edge
// provider and to resolve a --jam segment_id to its EdgeID.
func buildRouter(roadGraph graph.Graph, algo, congestionSource, jamSegment string, jamVPH float64) (routing.Router, error) {
	if algo == algoNaive {
		return routing.NewNaiveRouter(roadGraph), nil
	}
	provider, err := buildCongestionProvider(roadGraph, congestionSource, jamSegment, jamVPH)
	if err != nil {
		return nil, err
	}
	return routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider), nil
}

// buildCongestionProvider builds the frozen congestion snapshot the reactive
// router best-responds to (project-spec.md §R5). It chooses the source by
// congestionSource: "sim" is a fixed-seed simulator.New, a non-empty value is a
// file path decoded into a static.NewProvider (§3 batch), and empty is a zero-load
// memory.New. When jamSegment is non-empty its load (jamVPH) is injected onto the
// resolved edge so reactive diverts around it; the sim and empty sources support
// injection, while a file source already carries its own loads (a --jam alongside
// a file source is rejected as a contradiction rather than silently dropped).
//
// An unknown --jam segment_id is a clean user error naming the bad segment, not a
// silent no-op, so a typo cannot quietly produce the un-diverted route.
func buildCongestionProvider(roadGraph graph.Graph, congestionSource, jamSegment string, jamVPH float64) (congestion.CongestionProvider, error) {
	switch congestionSource {
	case congestionSim:
		sim := simulator.New(simulatorSeed, roadGraph.EdgeCount())
		if jamSegment != "" {
			edgeID, err := resolveJamSegment(roadGraph, jamSegment)
			if err != nil {
				return nil, err
			}
			sim.Inject(edgeID, jamVPH)
		}
		return sim, nil
	case "":
		mem := memory.New(roadGraph.EdgeCount())
		if jamSegment != "" {
			edgeID, err := resolveJamSegment(roadGraph, jamSegment)
			if err != nil {
				return nil, err
			}
			mem.Set(edgeID, jamVPH)
		}
		return mem, nil
	default:
		if jamSegment != "" {
			return nil, fmt.Errorf("cannot combine -jam with a file -congestion %q (the file already carries the loads); use -congestion sim to inject", congestionSource)
		}
		messages, err := domain.DecodeSegmentCongestionFile(congestionSource)
		if err != nil {
			return nil, fmt.Errorf("load congestion %q: %w", congestionSource, err)
		}
		index := static.BuildSegmentEdgeIndex(roadGraph)
		provider, err := static.NewProvider(messages, index, roadGraph.EdgeCount())
		if err != nil {
			return nil, fmt.Errorf("load congestion %q: %w", congestionSource, err)
		}
		return provider, nil
	}
}

// resolveJamSegment maps a --jam segment_id to its dense EdgeID via the graph's
// segment→edge index (static.BuildSegmentEdgeIndex). An unknown segment_id is a
// user error that NAMES the bad segment, so a typo fails loudly rather than
// no-opping into the un-jammed (un-diverted) route.
func resolveJamSegment(roadGraph graph.Graph, jamSegment string) (domain.EdgeID, error) {
	index := static.BuildSegmentEdgeIndex(roadGraph)
	edgeID, found := index[domain.SegmentID(jamSegment)]
	if !found {
		return 0, fmt.Errorf("invalid -jam %q: no such segment_id in the graph", jamSegment)
	}
	return edgeID, nil
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
