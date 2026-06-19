// Command benchmark is the Phase-1+ routing smoke/timing harness. It is NOT the
// full convergent-routing comparison across the six algorithms — that harness
// (demand-aware strategies, realized-time accounting, the headline improvement
// number) lands in a later phase. What this does today is honest and minimal:
// load the toy edge_attributes GeoJSON, build the naive (free-flow) router, run
// a small batch of real routing requests over the toy graph, time it, and print
// a short factual summary (nodes, edges, requests routed, elapsed). It then runs
// each of the four Phase-3 routers (incremental, msa, systemoptimal, multipath)
// over the SAME toy graph and the SAME request batch, each emitting its own
// deterministic summary line (router Name() + requests_routed). Those routers are
// on no CLI path, so this harness is the only place CI Lane A exercises them.
//
// Its job is to make CI Lane A genuinely gate routing: any load error or routing
// error exits non-zero, so a broken loader/graph/router fails the build rather
// than slipping through unit tests alone. Lane A also asserts each router name
// appears and routed the expected batch, so a router silently dropping out of the
// run (or routing the wrong count) fails the merge gate too.
//
// DETERMINISM: every summary line this harness emits must be byte-identical run
// to run, modulo two wall-clock fields: the slog text handler's own `time=` stamp
// on every line, and the naive line's `elapsed=` routing duration. The MSA and
// systemoptimal routers achieve this via sorted
// node/edge iteration (internal/routing/repro.go), never RNG; the multipath
// router's randomized split is made reproducible by threading a single fixed seed
// (benchSeed below) into its constructor — per-request seeding inside the router
// keeps a fixed seed ⇒ identical split regardless of worker scheduling. The
// in-process determinism test (main_test.go) runs the whole harness twice and
// asserts byte-identical output (after normalizing those two wall-clock fields), so
// any nondeterministic flow/ordering leaking from MSA averaging or the multipath
// split fails `go test -race ./...`.
//
// Diagnostics go through the internal slog logger, consistent with the rest of
// the engine. run() takes the destination writer so the determinism test can
// capture output in-process; main() wires it to os.Stderr.
package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/logging"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// toyNetworkPath is the shared toy edge_attributes fixture, resolved relative to
// engine/ (where `go run ./cmd/benchmark` and `make bench` run from).
const toyNetworkPath = "testdata/toy_network.geojson"

// benchSeed is the single fixed seed threaded into the multipath router so its
// randomized probabilistic split is reproducible: a fixed seed ⇒ identical split
// every run. Pinning it here (rather than seeding from the clock) is what makes
// the multipath summary line deterministic and lets the in-process determinism
// test (main_test.go) assert byte-identical output. The value is arbitrary but
// MUST stay fixed — changing it is a deliberate, reviewable change to the
// canonical bench run, not an accident of wall-clock time.
const benchSeed int64 = 20260618

// benchK is the per-request path count for the multipath router's Yen K-shortest
// split. Three is the same small value the multipath tests exercise — enough to
// demonstrate a split over the toy graph without enumerating every path. NOTE: on
// this sparse toy graph an OD pair may have fewer than benchK simple paths, in
// which case the probabilistic draw collapses onto the single available path and
// does not actually fan out. The determinism test still gates path-enumeration
// order and requests_routed here; it does not assert the split itself fires — that
// is covered by the multipath router's own unit tests (internal/routing).
const benchK = 3

// toyRequests returns the shared toy request batch every router in this harness
// routes. The coordinates match the toy fixture's documented endpoints (the toy
// network is largely one-way): bench-0 is origin node 0 -> destination node 2,
// exercising the lowest-cost (≠ fewest-hops) path the network was built to
// demonstrate (intentionally the same demo route as cmd/route's defaults);
// bench-1 is node 3 -> node 4 over the two-way secondary pair. These are this
// harness's own batch inputs — it asserts nothing about the resulting paths, so
// they are deliberately independent of the canonical-route golden.
func toyRequests() []routing.RouteRequest {
	return []routing.RouteRequest{
		{ID: "bench-0", From: domain.LatLon{Lat: 40.73, Lon: -73.99}, To: domain.LatLon{Lat: 40.74, Lon: -73.97}},
		{ID: "bench-1", From: domain.LatLon{Lat: 40.742, Lon: -73.965}, To: domain.LatLon{Lat: 40.745, Lon: -73.96}},
	}
}

func main() {
	// No logging.Setup() here: run() builds its own writer-bound logger and nothing
	// in the benchmark's call graph logs through slog.Default(), so installing a
	// global default would be dead configuration. If a router ever starts emitting
	// via slog.Default(), wire logging.Setup() back in (and thread its config into
	// run) so that output is captured by the determinism test too.
	if err := run(os.Stderr, toyNetworkPath); err != nil {
		os.Exit(1)
	}
}

// run loads the graph at graphPath, routes the shared batch through the naive
// router and each Phase-3 router, and writes one summary line per router to w. It
// returns a non-nil error (and logs it) on any load/routing failure so main can
// exit non-zero. Output to w is deterministic apart from two wall-clock fields (the
// slog `time=` stamp on every line and the naive line's `elapsed=`), so the
// determinism test can run this twice and compare. graphPath is a parameter (not
// the package const) only so the in-process
// test can resolve the fixture relative to the test's working directory; main
// always passes toyNetworkPath.
func run(w io.Writer, graphPath string) error {
	// A text logger writing to w, so output is captured in-process by tests and
	// goes to stderr in production. Info level matches the engine default.
	logger := logging.New(logging.Config{Writer: w, Format: logging.FormatText})

	// Load the toy network. A failure here is fatal: the bench cannot route
	// without a graph, and CI must see a non-zero exit.
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(graphPath)
	if err != nil {
		logger.Error("load toy network failed", "component", "benchmark", "path", graphPath, "err", err)
		return err
	}

	reqs := toyRequests()

	// Build the naive free-flow router and route the shared batch. This is the
	// original Phase-1 run; its summary line (including requests_routed=2) is the
	// one CI Lane A has greped since Phase 1 and is kept byte-for-byte unchanged.
	naive := routing.NewNaiveRouter(roadGraph)
	start := time.Now()
	routes, err := naive.Assign(context.Background(), reqs)
	elapsed := time.Since(start)
	if err != nil {
		logger.Error("routing failed", "component", "benchmark", "err", err)
		return err
	}

	// Honest summary: this is a Phase-1 naive-router run over the toy graph, not
	// the six-algorithm comparison. Report only what actually happened.
	logger.Info("phase-1 naive-router toy-graph bench",
		"component", "benchmark",
		"router", naive.Name(),
		"nodes", roadGraph.NodeCount(),
		"edges", roadGraph.EdgeCount(),
		"requests_routed", len(routes),
		"elapsed", elapsed.String(),
	)

	// The four Phase-3 routers over the SAME graph and SAME batch. Each emits a
	// deterministic summary line including its Name() and requests_routed — no
	// `elapsed` field, so these lines are byte-identical run to run and CI can
	// assert each router name + count directly. DefaultBPR returns a cost.BPR
	// (which also satisfies cost.CostFunction, used by the multipath router).
	bpr := cost.DefaultBPR()
	phase3 := []routing.Router{
		routing.NewIncrementalRouter(roadGraph, bpr),
		routing.NewMSARouter(roadGraph, bpr),
		routing.NewSystemOptimalRouter(roadGraph, bpr),
		routing.NewMultipathRouter(roadGraph, bpr, benchSeed, benchK),
	}
	for _, router := range phase3 {
		routes, err := router.Assign(context.Background(), reqs)
		if err != nil {
			logger.Error("routing failed", "component", "benchmark", "router", router.Name(), "err", err)
			return err
		}
		logger.Info("phase-3 router toy-graph bench",
			"component", "benchmark",
			"router", router.Name(),
			"nodes", roadGraph.NodeCount(),
			"edges", roadGraph.EdgeCount(),
			"requests_routed", len(routes),
		)
	}

	return nil
}
