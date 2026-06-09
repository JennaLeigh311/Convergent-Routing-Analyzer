package routing_test

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// fakeRouter is a no-op Router used to prove the port is satisfiable. The six
// real strategies (naive, reactive, incremental, msa, systemoptimal, multipath)
// land in Phase 3.
type fakeRouter struct{ name string }

func (r fakeRouter) Route(req routing.RouteRequest) (routing.Route, error) {
	return routing.Route{RequestID: req.ID}, nil
}

func (r fakeRouter) Assign(reqs []routing.RouteRequest) ([]routing.Route, error) {
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
	r := fakeRouter{name: "fake"}

	if r.Name() != "fake" {
		t.Errorf("Name() = %q, want %q", r.Name(), "fake")
	}

	reqs := []routing.RouteRequest{{ID: "a"}, {ID: "b"}}
	routes, err := r.Assign(reqs)
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
}
