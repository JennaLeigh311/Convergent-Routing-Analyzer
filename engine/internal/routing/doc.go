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
// snapshot's immutability keeps the closure pure); multipath's Yen k-shortest core
// will need an edge-exclusion set, added via a Graph wrapper rather than a change
// to the dijkstra signature.
package routing
