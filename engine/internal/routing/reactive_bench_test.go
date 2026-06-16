package routing

// These benchmarks are the gate for issue #67: they quantify the per-Route
// transient allocation that the read-only View borrow removes. The reactive
// congested-weight closure only READS per-edge load, yet the pre-#67 Route copied
// the whole dense load vector (a congestion.LoadSnapshot) on every single request.
// At city scale (~1–3M edges) that copy is ~8–24MB allocated + memcpy per request
// and, under heavy single-request concurrency, dominates the actual shortest-path
// work as a GC-pressure cliff. The benchmarks below run the SAME corner-to-corner
// reactive Route over the SAME city-scale grid, differing only in whether the
// congested weight is built from an owning Snapshot (the pre-#67 path, preserved
// in routeViaSnapshot) or a borrowed View (the post-#67 path the real Route uses),
// both with b.ReportAllocs(), so -benchmem shows the bytes/op and allocs/op #67
// removes. The headline is bytes/op: the dense load copy is a SINGLE large
// allocation (one float64 per edge), so removing it cuts allocs/op by exactly one
// but slashes bytes/op by edgeCount*8 — at 512x512 that is the ~8MB the Snapshot
// path shows over the View path (the remaining ~524K allocs/op are Dijkstra's
// per-settled-node heap pushes, identical on both paths). The win is the bytes the
// borrow stops allocating + memcpying per request, which is what feeds the
// under-concurrency GC cliff the issue describes.

import (
	"context"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/memory"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// benchReactiveGridSide sizes the synthetic city-scale grid for the Route
// allocation benchmarks: 512x512 = 262144 nodes and ~1.05M directed edges, which
// sits in the issue's representative ~1–3M-edge band — large enough that the dense
// LoadSnapshot copy (one float64 per edge, ~8MB) is a real allocation the borrow
// removes, not noise.
const benchReactiveGridSide = 512

// routeViaSnapshot is a frozen copy of the pre-#67 single-request Route path:
// identical to ReactiveRouter.Route except it builds the congested weight from an
// OWNING provider.Snapshot() (copying the whole dense load vector) instead of the
// borrowed provider.View(). It exists only so the benchmark can measure the
// per-Route allocation the View borrow removes; production Route must use View.
func (router *ReactiveRouter) routeViaSnapshot(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	snapshot := router.provider.Snapshot()
	return router.routeWith(ctx, req, router.congestedWeight(snapshot))
}

// newReactiveBenchRouter builds a city-scale reactive router plus a
// corner-to-corner request, shared by both Route benchmarks so they measure the
// same traversal over the same load. The memory provider is sized to the grid's
// edge count, so its Snapshot copies the full dense vector — the allocation under
// test.
func newReactiveBenchRouter(b *testing.B) (*ReactiveRouter, RouteRequest) {
	b.Helper()
	roadGraph := buildGridGraph(benchReactiveGridSide)
	provider := memory.New(roadGraph.EdgeCount())
	router := NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)

	origin, _ := roadGraph.Node(domain.NodeID(0))
	dest, _ := roadGraph.Node(domain.NodeID(benchReactiveGridSide*benchReactiveGridSide - 1))
	return router, RouteRequest{ID: "corner-to-corner", From: origin.Pos, To: dest.Pos}
}

// BenchmarkReactiveRouteSnapshot is the pre-#67 baseline: each single Route copies
// the whole dense LoadSnapshot up front. Its bytes/op and allocs/op are the waste
// #67 targets — the copy is ~8MB at this edge count.
func BenchmarkReactiveRouteSnapshot(b *testing.B) {
	router, req := newReactiveBenchRouter(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := router.routeViaSnapshot(ctx, req); err != nil {
			b.Fatalf("routeViaSnapshot: %v", err)
		}
	}
}

// BenchmarkReactiveRouteView is the post-#67 path: the real Route, which borrows a
// read-only View over the live load and copies nothing. Compared against
// BenchmarkReactiveRouteSnapshot under -benchmem it shows the bytes/op and
// allocs/op the borrow removes (the multi-MB dense-vector copy disappears,
// leaving only the unavoidable Dijkstra working-set allocations).
func BenchmarkReactiveRouteView(b *testing.B) {
	router, req := newReactiveBenchRouter(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := router.Route(ctx, req); err != nil {
			b.Fatalf("Route: %v", err)
		}
	}
}
