// Package benchmark holds the harness that runs route requests through each
// Router, the realized-travel-time evaluator, the discrete-time mesoscopic
// simulator (§R5), and the metrics (mean/p95, total time, Price of Anarchy,
// utilization, runtime).
//
// The realized total network travel time (Σ flow × BPR.Cost) is computed by
// routing.TotalNetworkTime — sourced intentionally from the routing package so the
// SO ≤ UE invariant test and this benchmark's Price-of-Anarchy metric share one
// implementation of the sum. This package builds the PoA ratio, percentiles, and
// runtime metrics on top of that single evaluator rather than re-deriving it.
//
// This package is currently a stub.
package benchmark
