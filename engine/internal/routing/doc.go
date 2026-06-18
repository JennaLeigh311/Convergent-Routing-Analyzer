// Package routing holds the Router strategy port and the six algorithm
// implementations (naive, reactive, incremental, msa, systemoptimal,
// multipath) over a shared Dijkstra/A* and Yen k-shortest core. The Router port
// is defined in routing.go. Phase 1 lands the shared binary-heap Dijkstra core
// (dijkstra.go) and the free-flow naive baseline (naive.go); Phase 2 adds the
// reactive best-response strategy (reactive.go), which weights edges by a BPR
// cost over a single frozen congestion snapshot; the remaining four demand-aware
// strategies (incremental, msa, systemoptimal, multipath) and the A*/Yen
// extensions arrive in Phase 3.
//
// Phase-3 extension notes: demand-aware strategies supply their congested weight
// by closing weightFunc over an immutable per-round congestion LoadSnapshot (the
// snapshot's immutability keeps the closure pure). The multipath strategy and its
// Yen k-shortest core (kshortest.go, multipath.go) are implemented: Yen excludes
// edges and nodes via +Inf-weight MASKING (maskedWeight wraps the base weightFunc to
// return +Inf for a removed edge or a removed node's out-edges) so it reuses the
// existing Dijkstra without changing its signature; multipath then splits the
// requests across the K paths with a per-request-seeded probabilistic rule and
// surfaces the split provenance on a MultipathResult adjunct (never by mutating the
// shared AssignResult).
//
// Iterative-router substrate (issue #71). The shared pieces every Phase-3
// iterative router (incremental, msa, systemoptimal, multipath) sits on are
// settled once here so the return shape and helper APIs are stable before any
// iterative router is written:
//
//   - AssignResult (routing.go) is the batch return shape: Routes, the dense
//     FinalFlows vector (indexed by EdgeID, length EdgeCount()), the achieved
//     convergence Gap, the Iters count, and Converged. Router gains an
//     AssignResult method (the real work) and keeps Assign as the paths-only face;
//     a router implements AssignResult and defines Assign as AssignFromResult over
//     it, so the two can never disagree. Single-pass routers (naive, reactive)
//     report Iters=1, Gap=0, Converged=true.
//   - prefetchOD (assign.go) resolves every request's endpoints to graph node ids
//     ONCE up front, so an iterative Assign never re-runs NearestNode inside its
//     per-request, per-iteration loop. newFlowVector + addRouteFlow turn chosen
//     paths into the FinalFlows vector the same way for every router.
//   - dijkstraScratchBuffer (scratch.go) is a per-goroutine reusable dist/prevEdge
//     arena so an iterative router does not allocate two O(NodeCount) slices per
//     Dijkstra. dijkstraScratch takes it; the single-call Route path passes nil and
//     is unchanged. A buffer is NOT safe for concurrent use — one per worker.
//   - repro.go is the reproducibility scaffolding: NewSeededRNG (one seed source),
//     SortedNodeIDs/SortedEdgeIDs (deterministic iteration — never
//     range a map on the assignment path, Go randomizes map order), and
//     WriteODSet/ReadODSet (serialize an OD set to disk and back). A fixed seed plus
//     a serialized OD set reproduces an Assign byte-for-byte.
package routing
