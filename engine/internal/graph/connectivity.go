package graph

// connectivity.go holds the loader's OPT-IN, NON-FATAL connectivity diagnostic
// (issue #82). It is the implementation half of WithConnectivityWarn: given the
// node/edge slices the loader has already validated and remapped to dense ids, it
// computes WEAK connectivity and, when the graph is not a single weakly-connected
// component, emits a single structured slog warning. It NEVER rejects an export
// and NEVER mutates the graph — it is a diagnostic, never a fail-closed gate. The
// loader's correctness invariants (§2 shape/range/length checks) stay in
// loader.go; this file is a separate, advisory concern kept out of the hot
// loading path so the default (option-off) load runs zero new code.
//
// WEAK vs STRONG — why weak. We treat every edge as UNDIRECTED (union From with
// To) and ask whether the resulting undirected graph is one component. This is
// deliberately the WEAK measure, not strong/directed connectivity, because the
// footgun we want to surface is a TOPOLOGICAL split — a bad bounding-box clip, or a
// physically separated region that the export accidentally carries as an island
// with no edge of EITHER direction bridging it (e.g. the adversarial fixture's
// {4,5} island). Strong connectivity would additionally flag every legitimate
// one-way SINK — a node reachable forward but with no edge leading back out, like
// the adversarial fixture's one-way trap node 3 — which is a normal, intended
// feature of a real road network (one-way streets, dead-end slip roads). Reporting
// those as "unreachable" would be pure noise and would fire on almost every real
// extract, training operators to ignore the warning. Weak connectivity fires only
// on a genuine, operationally interesting split, so it is the right diagnostic.
//
// Determinism. The whole project requires reproducible output, so this helper is
// deterministic: the "largest component" tie-break (on equal size) prefers the
// component containing the lowest node id, and the reported sample of unreachable
// nodes is ascending by construction (the collection loop walks node ids
// 0..nodeCount-1, appending in order). Two loads of the same artifact therefore
// produce a byte-identical warning.

import (
	"log/slog"
)

// connectivityCapSample bounds how many unreachable node ids the warning lists.
// The full unreachable COUNT is always reported (unreachable_nodes); the
// sample is capped so a heavily-fragmented export cannot emit an unbounded log
// line. The first capacity ids (ascending) are a representative, reproducible
// sample an operator can use to locate the split.
const connectivityCapSample = 10

// warnIfNotWeaklyConnected runs the opt-in connectivity diagnostic over the
// loader's already-validated, densely-remapped nodes and edges. It computes WEAK
// connectivity (edges treated as undirected) via union-find, finds the largest
// component, and — only if there is more than one component — emits a single
// non-fatal slog warning describing the fragmentation. It returns nothing and
// changes nothing: an unreachable component is surfaced, never rejected. A graph
// with zero or one node is trivially one component and emits nothing.
//
// The warning is sent to slog.Default() (the codebase logging idiom; see
// engine/internal/logging, installed by cmd entrypoints via logging.Setup()) at
// Warn level, with structured fields: the component count, the largest
// component's node count and fraction of the whole, the full count of nodes
// outside the largest component, and an ascending, capped sample of those
// unreachable node ids.
func warnIfNotWeaklyConnected(nodes []Node, edges []Edge) {
	nodeCount := len(nodes)
	if nodeCount < 2 {
		// Zero or one node is trivially a single (weak) component; nothing to warn.
		return
	}

	// Union-find (weak connectivity): union the endpoints of every edge,
	// IGNORING direction, so a one-way edge still merges its two endpoints into
	// one component. After the full pass, two nodes share a component iff some
	// chain of edges (in either direction) connects them.
	uf := newUnionFind(nodeCount)
	for index := range edges {
		uf.union(int(edges[index].From), int(edges[index].To))
	}

	// Tally each component by its canonical root, tracking — deterministically —
	// the largest. componentSize is keyed by root; lowestID records the smallest
	// member id seen per root so the tie-break (equal size → component with the
	// lowest node id) is reproducible regardless of iteration order.
	componentSize := make(map[int]int, nodeCount)
	lowestID := make(map[int]int, nodeCount)
	for node := 0; node < nodeCount; node++ {
		root := uf.find(node)
		componentSize[root]++
		if existing, ok := lowestID[root]; !ok || node < existing {
			lowestID[root] = node
		}
	}

	componentCount := len(componentSize)
	if componentCount <= 1 {
		// Fully weakly connected — the common, healthy case. Emit nothing so a
		// connected export is silent and the default load stays quiet.
		return
	}

	// Pick the largest component deterministically: by size descending, then (on
	// a size tie) by the component's lowest member id ascending, so "largest" is
	// reproducible across runs and map orderings.
	largestRoot := -1
	largestSize := -1
	for root, size := range componentSize {
		if size > largestSize || (size == largestSize && lowestID[root] < lowestID[largestRoot]) {
			largestRoot = root
			largestSize = size
		}
	}

	// Collect every node NOT in the largest component (ascending — the node loop
	// runs 0..nodeCount-1) — these are the "unreachable from the main component"
	// nodes. The full count is reported; the logged sample is capped.
	unreachable := make([]int, 0, nodeCount-largestSize)
	for node := 0; node < nodeCount; node++ {
		if uf.find(node) != largestRoot {
			unreachable = append(unreachable, node)
		}
	}

	sample := unreachable
	if len(sample) > connectivityCapSample {
		sample = sample[:connectivityCapSample]
	}

	fraction := float64(largestSize) / float64(nodeCount)

	slog.Default().Warn(
		"edge_attributes: graph is not weakly connected",
		"components", componentCount,
		"largest_component_nodes", largestSize,
		"largest_component_fraction", fraction,
		"unreachable_nodes", nodeCount-largestSize,
		"sample_unreachable_nodes", sample,
	)
}

// unionFind is the weak-connectivity disjoint-set structure: parent holds each
// node's set link and rank bounds the tree height for union-by-rank. It replaces
// the two parallel slices previously threaded through the call sites.
type unionFind struct {
	parent []int
	rank   []int
}

// newUnionFind returns an n-element disjoint-set with every node its own singleton.
func newUnionFind(n int) *unionFind {
	parent := make([]int, n)
	for index := range parent {
		parent[index] = index
	}
	return &unionFind{parent: parent, rank: make([]int, n)}
}

// find returns the canonical root of x's set, path-halving as it walks.
func (uf *unionFind) find(x int) int {
	for uf.parent[x] != x {
		uf.parent[x] = uf.parent[uf.parent[x]]
		x = uf.parent[x]
	}
	return x
}

// union merges the sets containing a and b using union-by-rank (no-op if already joined).
func (uf *unionFind) union(a, b int) {
	rootA, rootB := uf.find(a), uf.find(b)
	if rootA == rootB {
		return
	}
	switch {
	case uf.rank[rootA] < uf.rank[rootB]:
		uf.parent[rootA] = rootB
	case uf.rank[rootA] > uf.rank[rootB]:
		uf.parent[rootB] = rootA
	default:
		uf.parent[rootB] = rootA
		uf.rank[rootA]++
	}
}
