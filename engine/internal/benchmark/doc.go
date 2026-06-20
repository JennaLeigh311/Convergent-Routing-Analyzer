// Package benchmark holds the realized-travel-time evaluator and the comparison
// metrics (mean/p95, total network time, Price of Anarchy, edge-utilization
// balance) that turn a router's AssignResult into the headline numbers the
// project reports (project-spec.md §5, §R5).
//
// The central honesty distinction §5 insists on is ROUTING COST vs REALIZED
// TRAVEL TIME. Route.CostS is the weight a router optimized its path against (e.g.
// free-flow time for naive, marginal cost for systemoptimal) — NOT the time a
// driver actually experiences. The realized travel time is computed HERE, after
// assignment, by applying the BPR cost function to the ACTUAL per-edge flow the
// assignment produced (AssignResult.FinalFlows). Two routers can pick the same
// path yet realize different times because they load the network differently; only
// the realized time is comparable across strategies.
//
// The realized total network travel time (Σ flow × BPR.Cost) is computed by
// routing.TotalNetworkTime — sourced intentionally from the routing package so the
// SO ≤ UE invariant test and this benchmark's Price-of-Anarchy metric share one
// implementation of the weighted-flow sum. This package builds the per-request
// realized times, the percentiles, the utilization balance, and the PoA ratio on
// top of that single evaluator rather than re-deriving the sum.
//
// Everything here is DETERMINISTIC: every aggregate iterates a sorted copy of its
// input (never a map, never the caller's slice in place), so a fixed seed plus a
// serialized OD set reproduces a byte-identical Result run to run — the criterion
// the CLI table (Phase 4) and the live API (Phase 5) both depend on, since both
// consume this package as their analytics surface rather than re-deriving metrics
// inside the CLI or the handler.
//
// What this package deliberately does NOT do: it does not construct routers, run
// the demand sweep, render the CLI table, or speak HTTP/WebSocket. It is a pure,
// reusable metrics surface — the caller supplies the graph, the BPR, and the
// AssignResult(s); the package returns numbers. Router construction is the
// caller's job so the evaluator stays router-agnostic (PriceOfAnarchy takes two
// totals/results, it never hardcodes naive-vs-systemoptimal inside the metric).
package benchmark
