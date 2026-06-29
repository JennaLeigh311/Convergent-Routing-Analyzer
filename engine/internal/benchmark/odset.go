package benchmark

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// DefaultODCount is the default request count R the Phase-4 demand sweep
// synthesizes per demand level (project-spec.md §R5 calls for ~1000 OD pairs).
// It is a sweep knob, not a contract: the CLI may override it, but the canonical
// `make bench` run pins it so the published table is reproducible.
const DefaultODCount = 1000

// ODPair is one origin→destination template the generator draws from: a pair of
// graph node ids that is KNOWN reachable on the target network, plus the human
// labels (origin/destination segment_id-ish names) used in the serialized OD set
// and the table. It deliberately carries NO domain.EdgeID — the frozen-contract
// rule (§2) is that EdgeID is an internal index that must never leak to a
// serialized artifact, so the OD set on disk keys endpoints by node id + label,
// never by edge index.
type ODPair struct {
	// FromNode / ToNode are the resolved graph node ids. They are stable for a
	// given fixture (the loader assigns dense node ids deterministically from the
	// GeoJSON), so serializing them is reproducible — but they are network-internal
	// node ids, not the §2 segment_id frozen key, so a consumer treats them as an
	// opaque endpoint handle for replay, not as a cross-fixture identifier.
	FromNode domain.NodeID `json:"from_node"`
	ToNode   domain.NodeID `json:"to_node"`

	// Label is a human-readable origin→destination tag (e.g. "n0->n2") used in
	// diagnostics and to make the serialized OD set self-describing. It is derived
	// from the node ids, never from any EdgeID.
	Label string `json:"label"`
}

// ODRequest is one synthesized request in a serialized OD set: a stable request id,
// the origin/destination LATLON the router snaps (so a replay is independent of how
// node ids happen to be numbered), and the per-request demand weight in
// vehicles/hour. It is the on-disk shape — JSON-tagged, EdgeID-free, and keyed by
// coordinates + a derived label rather than any internal edge index.
type ODRequest struct {
	ID        string        `json:"id"`
	From      domain.LatLon `json:"from"`
	To        domain.LatLon `json:"to"`
	WeightVPH float64       `json:"weight_vph"`
	Label     string        `json:"label"`
}

// ODSet is a reproducible, serializable set of OD requests for one demand level.
// It is deterministic for a given (seed, count, total demand): GenerateODSet draws
// pair templates from a fixed reachable-pair pool with a single seeded RNG and
// fixed iteration order, so two generations with the same parameters produce an
// == ODSet and byte-identical JSON.
//
// It serializes ONLY coordinates, weights, labels, and node ids — never a
// domain.EdgeID — so the artifact honors the §2 "EdgeID never leaks to a
// serialized output" rule. The set is replayed by feeding Requests' coordinates
// back through any router's Assign/AssignResult.
type ODSet struct {
	// Seed is the RNG seed the set was generated with, recorded so the artifact is
	// self-describing and a re-generation can be reproduced exactly.
	Seed int64 `json:"seed"`

	// DemandLevel is the human label for the demand scenario (e.g. "light",
	// "congested"). It mirrors Result.DemandLevel so the OD set and the table cells
	// share a column identity.
	DemandLevel string `json:"demand_level"`

	// TargetVC is the volume/capacity band this set was sized to drive the busiest
	// reference corridor toward, recorded for honesty: the demand sweep documents
	// HOW the target v/c is achieved, and the achieved v/c is reported in the table
	// alongside it (it is a target, not a guarantee — the toy graph is sparse).
	TargetVC float64 `json:"target_vc"`

	// TotalDemandVPH is the summed weight over all requests, vehicles/hour. It is
	// the knob the sweep scales (together with CapacityScale) to hit TargetVC.
	TotalDemandVPH float64 `json:"total_demand_vph"`

	// Requests is the synthesized request list, in deterministic generation order.
	Requests []ODRequest `json:"requests"`
}

// reachablePairPool returns the fixed pool of origin→destination node pairs that
// are KNOWN reachable on the graph, each carrying its node ids and a derived label.
// It is built by a deterministic scan: for every ordered (from, to) node pair it
// runs reachable (a BFS over OutEdgeIDs in ascending edge-id order), keeping only
// the reachable ones, in ascending (from, to) node-id order. Unreachable pairs (the
// toy network is largely one-way) are dropped here so the generator never
// synthesizes a request that would fail to route — the "unreachable OD pairs handled
// cleanly, never panic/NaN" criterion is satisfied by construction.
//
// The pool excludes self-pairs (from == to): a zero-edge route realizes 0 s and
// carries no demand, so it would only dilute the sweep without exercising any edge.
func reachablePairPool(g graph.Graph) []ODPair {
	nodeCount := g.NodeCount()
	pairs := make([]ODPair, 0, nodeCount*nodeCount)
	for from := 0; from < nodeCount; from++ {
		for to := 0; to < nodeCount; to++ {
			if from == to {
				continue
			}
			if !reachable(g, domain.NodeID(from), domain.NodeID(to)) {
				continue
			}
			// Confirm both endpoints resolve; the resolved positions are re-fetched
			// in GenerateODSet when a request actually draws this pair.
			if _, fromOK := g.Node(domain.NodeID(from)); !fromOK {
				continue
			}
			if _, toOK := g.Node(domain.NodeID(to)); !toOK {
				continue
			}
			pairs = append(pairs, ODPair{
				FromNode: domain.NodeID(from),
				ToNode:   domain.NodeID(to),
				Label:    fmt.Sprintf("n%d->n%d", from, to),
			})
		}
	}
	return pairs
}

// reachable reports whether a directed path exists from → to via a deterministic
// BFS over OutEdgeIDs (ascending edge-id order). It is the generator's own
// reachability check (rather than constructing a router) so the pool can be built
// before any BPR/router is chosen and stays purely a graph-topology question.
func reachable(g graph.Graph, from, to domain.NodeID) bool {
	if from == to {
		return true
	}
	visited := make(map[domain.NodeID]bool, g.NodeCount())
	queue := []domain.NodeID{from}
	visited[from] = true
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, edgeID := range g.OutEdgeIDs(node) {
			edge, ok := g.Edge(edgeID)
			if !ok {
				continue
			}
			if edge.To == to {
				return true
			}
			if !visited[edge.To] {
				visited[edge.To] = true
				queue = append(queue, edge.To)
			}
		}
	}
	return false
}

// GenerateODSet synthesizes a reproducible OD set of `count` requests over the
// graph's reachable-pair pool, sized so the summed demand equals totalDemandVPH and
// labeled with demandLevel/targetVC for the table. It is DETERMINISTIC for a given
// (g, seed, count): a single math/rand.Rand seeded by `seed` picks each request's
// pair from the fixed pool, and the per-request weight is totalDemandVPH spread
// evenly over the requests, so two calls with the same arguments return an ==
// ODSet and identical JSON (the §R5 reproducibility criterion).
//
// A non-positive count yields an empty (but non-nil) request slice — the empty-batch
// case every downstream metric is defined on. If the graph has no reachable pair at
// all (a fully disconnected fixture) the set is also empty rather than an error:
// there is simply nothing to route, and the evaluator's empty-batch contract carries
// it through to a finite, zero Result.
func GenerateODSet(g graph.Graph, seed int64, count int, demandLevel string, targetVC, totalDemandVPH float64) ODSet {
	set := ODSet{
		Seed:           seed,
		DemandLevel:    demandLevel,
		TargetVC:       targetVC,
		TotalDemandVPH: totalDemandVPH,
		Requests:       []ODRequest{},
	}
	if count <= 0 {
		return set
	}
	pool := reachablePairPool(g)
	if len(pool) == 0 {
		return set
	}

	// Per-request weight: spread the total demand evenly so the summed FinalFlows
	// the routers accumulate is totalDemandVPH (modulo which pairs share edges). A
	// uniform weight keeps the generator's only randomness in WHICH pair each
	// request draws, which is the reproducibility-critical choice.
	weight := totalDemandVPH / float64(count)

	rng := rand.New(rand.NewSource(seed))
	requests := make([]ODRequest, 0, count)
	for i := 0; i < count; i++ {
		pair := pool[rng.Intn(len(pool))]
		fromNode, _ := g.Node(pair.FromNode)
		toNode, _ := g.Node(pair.ToNode)
		requests = append(requests, ODRequest{
			ID:        fmt.Sprintf("od-%04d", i),
			From:      fromNode.Pos,
			To:        toNode.Pos,
			WeightVPH: weight,
			Label:     pair.Label,
		})
	}
	set.Requests = requests
	return set
}

// RouteRequests converts the serialized OD set into the routing.RouteRequest batch
// the routers consume, in stored order. The per-request Weight carries the demand
// (vehicles/hour) so FinalFlows accumulates real load; DepartAt is left zero
// (static assignment — every request departs at t=0), matching how the iterative
// routers treat the batch. The static demand sweep (RunSweep / RunSingle) uses this
// all-at-once form; the live time-of-day path uses RouteRequestsAt to spread departures.
func (s ODSet) RouteRequests() []routing.RouteRequest {
	reqs := make([]routing.RouteRequest, len(s.Requests))
	for i, r := range s.Requests {
		reqs[i] = routing.RouteRequest{
			ID:     r.ID,
			From:   r.From,
			To:     r.To,
			Weight: r.WeightVPH,
		}
	}
	return reqs
}

// RouteRequestsAt is RouteRequests with the DepartAt field SPREAD across the demand
// window [start, start+windowSeconds] following the diurnal release curve (demand.go),
// instead of the all-at-t=0 static burst RouteRequests produces. It is the seam that
// makes the §R5 mesoscopic simulator's release machinery (DepartAt ≤ clock) actually
// fire over time, so congestion builds as the peak fills and drains as it empties
// (issue #111). The spread is deterministic — a pure function of (start, window, count)
// via departureSpread, no RNG — so a fixed (StartTime, Seed, Count) still yields a
// byte-identical run.
//
// A non-positive windowSeconds degrades to the static all-at-t=0 batch (every DepartAt
// is 0), so a caller can opt out of time-spreading by passing window ≤ 0.
func (s ODSet) RouteRequestsAt(start time.Time, windowSeconds float64) []routing.RouteRequest {
	reqs := s.RouteRequests()
	departs := departureSpread(start, windowSeconds, len(reqs))
	for i := range reqs {
		reqs[i].DepartAt = departs[i]
	}
	return reqs
}

// WriteJSON serializes the OD set to w as indented JSON (deterministic field order
// from the struct tags, requests in generation order), so a saved OD artifact is
// both diffable and exactly reproducible. It does not write any EdgeID (the struct
// carries none), honoring the §2 frozen-contract rule.
func (s ODSet) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// LabelCounts returns the per-label request multiplicity in sorted label order, a
// small deterministic summary used by tests and diagnostics to assert the draw is
// reproducible without pinning all R requests. The map is collapsed to a sorted
// slice so the result is stable run to run.
func (s ODSet) LabelCounts() []LabelCount {
	counts := make(map[string]int, len(s.Requests))
	for _, r := range s.Requests {
		counts[r.Label]++
	}
	labels := make([]string, 0, len(counts))
	for label := range counts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	out := make([]LabelCount, 0, len(labels))
	for _, label := range labels {
		out = append(out, LabelCount{Label: label, Count: counts[label]})
	}
	return out
}

// LabelCount is one (origin→destination label, request count) entry of an OD set's
// label histogram, in sorted-label order.
type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}
