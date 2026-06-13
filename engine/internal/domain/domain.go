// Package domain holds the shared value types and identifiers used across every
// engine package. It is a leaf: it imports nothing from the rest of the engine
// and may be imported by all other packages. Keeping IDs here (rather than in
// graph) avoids forcing congestion/api/cost to depend on graph just for an ID
// type, and keeps the dependency direction clean.
package domain

import "strconv"

// NodeID is a compact internal identifier for a graph node (an intersection /
// OSM node). It is in-memory only and assigned at load time.
type NodeID int32

// EdgeID is a compact internal identifier for a directed graph edge. It is the
// in-memory key used on hot paths; the stable cross-system wire key is
// SegmentID.
//
// EdgeIDs are assigned densely and contiguously (0..EdgeCount-1) at load time so
// they can directly index flat per-edge slices (e.g. congestion.LoadSnapshot)
// without hashing. Every graph loader and CongestionProvider must honor this.
type EdgeID int32

// SegmentID is the canonical, stable, directed cross-system identifier for a
// road segment: "{osm_way_id}:{seq}:{dir}" with dir in {F,R}. It is the only
// road identifier shared across the Go engine, the Spark pipeline, and the
// frontend. See docs/contracts.md §1.
type SegmentID string

// Direction encodes travel direction along an OSM way.
type Direction uint8

const (
	// Forward runs along the way's node ordering ('F' in a SegmentID).
	Forward Direction = iota
	// Reverse runs against the way's node ordering ('R' in a SegmentID).
	Reverse
)

// String renders a Direction as its SegmentID token ("F"/"R"). An out-of-range
// value renders as "Direction(n)" rather than silently masquerading as Forward,
// so a bad value surfaces instead of producing a valid-looking wire key.
func (direction Direction) String() string {
	switch direction {
	case Forward:
		return "F"
	case Reverse:
		return "R"
	default:
		return "Direction(" + strconv.Itoa(int(direction)) + ")"
	}
}

// LatLon is a WGS84 coordinate in decimal degrees.
type LatLon struct {
	Lat float64
	Lon float64
}
