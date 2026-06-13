package routing_test

import (
	"context"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// fakeRouter is a no-op Router used to prove the port is satisfiable. The six
// real strategies (naive, reactive, incremental, msa, systemoptimal, multipath)
// land in Phase 3.
type fakeRouter struct{ name string }

func (router fakeRouter) Route(_ context.Context, req routing.RouteRequest) (routing.Route, error) {
	return routing.Route{RequestID: req.ID}, nil
}

func (router fakeRouter) Assign(_ context.Context, reqs []routing.RouteRequest) ([]routing.Route, error) {
	out := make([]routing.Route, len(reqs))
	for index, req := range reqs {
		out[index] = routing.Route{RequestID: req.ID}
	}
	return out, nil
}

func (router fakeRouter) Name() string { return router.name }

// Compile-time assertion: fakeRouter satisfies the Router port.
var _ routing.Router = fakeRouter{}

func TestFakeRouterSatisfiesPort(test *testing.T) {
	ctx := context.Background()
	router := fakeRouter{name: "fake"}

	if router.Name() != "fake" {
		test.Errorf("Name() = %q, want %q", router.Name(), "fake")
	}

	// Single-request Route propagates the request ID.
	if route, err := router.Route(ctx, routing.RouteRequest{ID: "x"}); err != nil || route.RequestID != "x" {
		test.Fatalf("Route() = (%+v, %v), want RequestID=x, nil", route, err)
	}

	// Batch Assign preserves order and length.
	reqs := []routing.RouteRequest{{ID: "a"}, {ID: "b"}}
	routes, err := router.Assign(ctx, reqs)
	if err != nil {
		test.Fatalf("Assign() error = %v", err)
	}
	if len(routes) != len(reqs) {
		test.Fatalf("Assign() returned %d routes, want %d", len(routes), len(reqs))
	}
	for index, want := range []string{"a", "b"} {
		if routes[index].RequestID != want {
			test.Errorf("routes[%d].RequestID = %q, want %q", index, routes[index].RequestID, want)
		}
	}

	// Empty input yields an empty (non-error) result.
	if routes, err := router.Assign(ctx, nil); err != nil || len(routes) != 0 {
		test.Errorf("Assign(nil) = (%v, %v), want empty, nil", routes, err)
	}
}
