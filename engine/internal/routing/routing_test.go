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

func (r fakeRouter) Route(_ context.Context, req routing.RouteRequest) (routing.Route, error) {
	return routing.Route{RequestID: req.ID}, nil
}

func (r fakeRouter) Assign(_ context.Context, reqs []routing.RouteRequest) ([]routing.Route, error) {
	out := make([]routing.Route, len(reqs))
	for i, req := range reqs {
		out[i] = routing.Route{RequestID: req.ID}
	}
	return out, nil
}

func (r fakeRouter) Name() string { return r.name }

// Compile-time assertion: fakeRouter satisfies the Router port.
var _ routing.Router = fakeRouter{}

func TestFakeRouterSatisfiesPort(t *testing.T) {
	ctx := context.Background()
	r := fakeRouter{name: "fake"}

	if r.Name() != "fake" {
		t.Errorf("Name() = %q, want %q", r.Name(), "fake")
	}

	// Single-request Route propagates the request ID.
	if rt, err := r.Route(ctx, routing.RouteRequest{ID: "x"}); err != nil || rt.RequestID != "x" {
		t.Fatalf("Route() = (%+v, %v), want RequestID=x, nil", rt, err)
	}

	// Batch Assign preserves order and length.
	reqs := []routing.RouteRequest{{ID: "a"}, {ID: "b"}}
	routes, err := r.Assign(ctx, reqs)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(routes) != len(reqs) {
		t.Fatalf("Assign() returned %d routes, want %d", len(routes), len(reqs))
	}
	for i, want := range []string{"a", "b"} {
		if routes[i].RequestID != want {
			t.Errorf("routes[%d].RequestID = %q, want %q", i, routes[i].RequestID, want)
		}
	}

	// Empty input yields an empty (non-error) result.
	if routes, err := r.Assign(ctx, nil); err != nil || len(routes) != 0 {
		t.Errorf("Assign(nil) = (%v, %v), want empty, nil", routes, err)
	}
}
