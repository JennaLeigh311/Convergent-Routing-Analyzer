package routing

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"

// RouteRequest is a single origin→destination navigation request.
type RouteRequest struct {
	ID   string
	From domain.LatLon
	To   domain.LatLon

	// DepartAt is the simulated departure time in seconds from the start of a
	// run. It is consumed by the mesoscopic simulator; a zero value means the
	// request departs at t=0 (the static-assignment case treats all requests as
	// simultaneous).
	DepartAt float64
}

// Route is a computed path for one request.
type Route struct {
	RequestID string

	// Edges is the ordered list of directed edges from origin to destination.
	Edges []domain.EdgeID

	// CostS is the routing cost in seconds the algorithm optimized against — the
	// weights it chose the path under. This is NOT the realized travel time; the
	// benchmark computes that separately by applying the cost function to the
	// final edge flows (see docs/benchmarks.md).
	CostS float64
}

// Router is a routing strategy. Route answers a single request; Assign solves
// the convergent (batch) problem for many simultaneous requests. Strategies
// differ only in the weights they feed the shared shortest-path core and how
// they iterate — see docs/algorithms.md for the six implementations.
//
// Assign is first-class (not a loop over Route) because demand-aware routing is
// fundamentally a batch problem: the assignment of one request affects the cost
// seen by the others.
type Router interface {
	Route(req RouteRequest) (Route, error)
	Assign(reqs []RouteRequest) ([]Route, error)
	Name() string
}
