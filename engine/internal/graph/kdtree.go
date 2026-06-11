package graph

import (
	"math"
	"sort"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// kdTree is a static, balanced, 2-d k-d tree over WGS84 points, built once and
// thereafter read-only. It is a reusable indexed-point spatial primitive: it
// indexes generic (position, payload-index) pairs rather than being welded to
// NearestNode (node lookup today; an edge-sample index later), so the same
// nearest-neighbor search can be reused for a different point set. It owns no
// graph types.
//
// Concurrency: after build the tree is never mutated — search allocates nothing
// and writes nothing shared — so any number of goroutines may query it without
// synchronization, satisfying the R5 immutable-shared-graph model.
//
// # Pruning admissibility (why the search is exact)
//
// The query metric is haversine (great-circle meters). The tree splits on raw
// lat/lon degrees, so at each internal node we must convert a one-axis
// coordinate gap into a LOWER BOUND on the haversine distance to ANY point on
// the far side of the splitting plane. If that lower bound exceeds the current
// best distance we may prune; for the result to equal brute-force-haversine the
// bound must never exceed the true distance (it may only under-estimate).
//
//   - Latitude axis: for any two points, the great-circle central angle is at
//     least the difference of their latitudes, so the distance is at least
//     R·|Δφ| (Δφ in radians). A point on the far side differs in latitude from
//     the query by at least the lat gap, hence haversine ≥ R·gapRad. Admissible
//     and tight in the pure-meridional case.
//
//   - Longitude axis: for two points differing by Δλ in longitude, the haversine
//     term is h = sin²(Δφ/2) + cosφ_q·cosφ_p·sin²(Δλ/2) ≥ cosφ_q·cosφ_p·sin²(Δλ/2),
//     where φ_q is the query latitude and φ_p the far point's. cos is positive and
//     decreasing in |latitude|, so cosφ_q·cosφ_p ≥ cos²(max(maxAbsLat, |φ_q|)):
//     every tree latitude satisfies |φ_p| ≤ maxAbsLat ≤ max(maxAbsLat, |φ_q|), and
//     trivially |φ_q| ≤ max(maxAbsLat, |φ_q|), so cos(max(maxAbsLat, |φ_q|)) is
//     ≤ both cosφ_q and cosφ_p. Hence the distance is at least
//     2R·asin(cos(max(maxAbsLat, |φ_q|))·sin(|Δλ|/2)). This is the EXACT
//     small-circle-to-great-circle conversion, not the linear R·cosφ·Δλ arc-length
//     approximation — the latter slightly OVER-estimates (a great circle shortcuts
//     across a parallel) and would not be admissible. Folding the query latitude
//     into the cosine factor keeps the bound admissible for EVERY query, including
//     one outside the node latitude band: a tree-wide maxAbsLat alone would
//     over-estimate (and could mis-prune) when |φ_q| exceeds maxAbsLat, since then
//     cosφ_q < cos(maxAbsLat). The factor is a single O(1) value per query.
//
// Both per-axis bounds are themselves lower bounds on the full great-circle
// distance (each ignores the other axis's contribution), so pruning on them
// never discards the true nearest neighbor.
//
// # Precondition: no ±180° antimeridian crossing
//
// The result equals brute-force-haversine-nearest for any query provided the
// indexed point set does not span the ±180° antimeridian (equivalently: it lies
// within a <180°-wide longitude band), which holds for any single-region road
// network. The longitude pruning bound is computed from the raw longitude gap in
// degrees, so a seam-crossing point set could mis-prune the true nearest: a
// far-side point near the opposite seam can be closer than its raw gap-to-plane
// suggests. This is a documented limitation, not a handled case.
type kdTree struct {
	pts       []kdPoint
	nodes     []kdNode
	root      int32   // index into nodes, or -1 when empty
	maxAbsLat float64 // max abs latitude (degrees) over tree points; feeds the longitude bound
}

// kdPoint is one indexed position in the tree: a coordinate plus the caller's
// opaque index (here a NodeID, in Phase 7 an edge-sample index).
type kdPoint struct {
	pos domain.LatLon
	idx int32
}

// kdNode is an internal tree node referencing its point by index into kdTree.pts
// and its children by index into kdTree.nodes (-1 for absent). axis is 0 for a
// latitude split, 1 for a longitude split.
type kdNode struct {
	point       int32
	left, right int32
	axis        uint8
}

// newKDTree builds a balanced k-d tree over pts. It takes ownership of and
// reorders the pts slice. An empty input yields a tree whose search always
// misses. The build is O(n log²n): a sort.Slice runs at every level to find the
// median (n log n elements sorted across log n levels). This is a one-time
// startup cost and acceptable at city scale; a quickselect partition could make
// it O(n log n) if startup latency ever matters.
func newKDTree(pts []kdPoint) *kdTree {
	t := &kdTree{pts: pts, root: -1}
	if len(pts) == 0 {
		return t
	}

	// maxAbsLat bounds the tree latitudes; combined with the query latitude it
	// yields the conservative longitude-cosine factor (see the type doc's
	// pruning-admissibility note).
	for i := range pts {
		if a := math.Abs(pts[i].pos.Lat); a > t.maxAbsLat {
			t.maxAbsLat = a
		}
	}

	t.nodes = make([]kdNode, 0, len(pts))
	idxs := make([]int32, len(pts))
	for i := range idxs {
		idxs[i] = int32(i)
	}
	t.root = t.build(idxs, 0)
	return t
}

// build recursively partitions the point indices idxs by the median on the
// current axis, appends a kdNode for that median, and returns its node index (or
// -1 for an empty range). depth selects the splitting axis (lat/lon alternate).
func (t *kdTree) build(idxs []int32, depth int) int32 {
	if len(idxs) == 0 {
		return -1
	}
	axis := uint8(depth % 2)
	sort.Slice(idxs, func(a, b int) bool {
		return t.coord(idxs[a], axis) < t.coord(idxs[b], axis)
	})
	mid := len(idxs) / 2

	// Reserve this node's slot before recursing so child indices stay stable.
	self := int32(len(t.nodes))
	t.nodes = append(t.nodes, kdNode{point: idxs[mid], axis: axis})
	left := t.build(idxs[:mid], depth+1)
	right := t.build(idxs[mid+1:], depth+1)
	t.nodes[self].left = left
	t.nodes[self].right = right
	return self
}

// coord returns the splitting coordinate (degrees) of point pi on the given axis.
func (t *kdTree) coord(pi int32, axis uint8) float64 {
	if axis == 0 {
		return t.pts[pi].pos.Lat
	}
	return t.pts[pi].pos.Lon
}

// nearest returns the indexed point closest to q by haversine distance, and
// ok=false for an empty tree. The result equals brute-force-haversine-nearest
// for any query provided the indexed point set does not span the ±180°
// antimeridian (see the type doc on pruning admissibility and its precondition).
func (t *kdTree) nearest(q domain.LatLon) (idx int32, ok bool) {
	if t.root < 0 {
		return 0, false
	}
	bestIdx := int32(-1)
	bestDist := math.MaxFloat64
	// lonCos = cos(max(maxAbsLat, |q.Lat|)) ≤ cosφ_q and ≤ cosφ_p for every tree
	// point, so the longitude lower bound never over-estimates (see type doc).
	// Computed once per query (O(1)) and threaded into the longitude bound.
	lonCos := math.Cos(degToRad(math.Max(t.maxAbsLat, math.Abs(q.Lat))))
	t.search(t.root, q, lonCos, &bestIdx, &bestDist)
	// A NaN in q makes every haversine NaN, so no comparison ever beats the
	// initial best and bestIdx stays -1; report a miss rather than indexing
	// t.pts[-1]. Also defends any future path that finds nothing.
	if bestIdx < 0 {
		return 0, false
	}
	return t.pts[bestIdx].idx, true
}

// search is the recursive branch-and-bound nearest-neighbor walk: descend toward
// the query, update the best, then visit the far child only if its splitting
// plane is within the current best distance (the admissible lower bound).
func (t *kdTree) search(n int32, q domain.LatLon, lonCos float64, bestIdx *int32, bestDist *float64) {
	if n < 0 {
		return
	}
	nd := t.nodes[n]
	p := t.pts[nd.point]

	if d := haversine(q, p.pos); d < *bestDist {
		*bestDist = d
		*bestIdx = nd.point
	}

	// Per-axis signed gap from the query to this node's splitting plane, in
	// degrees. Negative ⇒ query is on the "left" (smaller-coordinate) side.
	var gap float64
	if nd.axis == 0 {
		gap = q.Lat - p.pos.Lat
	} else {
		gap = q.Lon - p.pos.Lon
	}

	near, far := nd.left, nd.right
	if gap > 0 {
		near, far = nd.right, nd.left
	}

	t.search(near, q, lonCos, bestIdx, bestDist)

	// Visit the far side only if its plane could hold something nearer than the
	// current best. planeDist is the admissible lower bound on the haversine
	// distance to anything beyond the plane.
	if planeDist := t.axisLowerBound(nd.axis, gap, lonCos); planeDist < *bestDist {
		t.search(far, q, lonCos, bestIdx, bestDist)
	}
}

// axisLowerBound converts a per-axis coordinate gap (degrees) into a lower bound
// (meters) on the haversine distance to any point on the far side of that axis's
// splitting plane. See the kdTree type doc for the admissibility argument:
// latitude uses the exact R·|Δφ|; longitude uses the exact small-circle bound
// 2R·asin(lonCos·sin(|Δλ|/2)), where lonCos = cos(max(maxAbsLat, |query lat|)) is
// the per-query cosine factor ≤ both cosφ_q and cosφ_p, so it never over-estimates.
func (t *kdTree) axisLowerBound(axis uint8, gapDeg, lonCos float64) float64 {
	gapRad := math.Abs(degToRad(gapDeg))
	if axis == 0 {
		return earthRadiusM * gapRad
	}
	return 2 * earthRadiusM * math.Asin(lonCos*math.Sin(gapRad/2))
}
