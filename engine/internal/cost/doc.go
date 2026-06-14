// Package cost holds the CostFunction port and its volume-delay
// implementations. The port is defined in cost.go; two concrete implementations
// satisfy it:
//
//   - BPR (bpr.go): the reference Bureau of Public Roads cost,
//     t(v) = t_free * (1 + alpha*(v/c)^beta), plus the marginal cost
//     t_free * (1 + alpha*(beta+1)*(v/c)^beta) the system-optimal router routes on.
//   - Linear (linear.go): the simpler linear-in-(v/c) alternative,
//     t(v) = t_free * (1 + slope*(v/c)).
//
// Both share a single global capacity_scale knob (project-spec.md §R3) and the
// vehicles/hour unit contract for load and capacity (docs/contracts.md §3).
package cost
