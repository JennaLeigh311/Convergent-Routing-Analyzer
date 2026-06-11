// Package routing holds the Router strategy port and the six algorithm
// implementations (naive, reactive, incremental, msa, systemoptimal,
// multipath) over a shared Dijkstra/A* and Yen k-shortest core. The Router port
// is defined in routing.go. Phase 1 lands the shared binary-heap Dijkstra core
// (dijkstra.go) and the free-flow naive baseline (naive.go); the five
// demand-aware strategies and the A*/Yen extensions arrive in Phase 3.
package routing
