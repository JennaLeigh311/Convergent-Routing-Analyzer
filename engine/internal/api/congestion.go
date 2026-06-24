package api

import (
	"net/http"
	"sort"
)

// endpointCongestion is the metric label for the congestion snapshot endpoint.
const endpointCongestion = "congestion"

// congestionResponse is the GET /congestion body: the current per-segment load
// snapshot. It is keyed by segment_id (the frozen §1 contract), NOT by the dense
// in-memory EdgeID, so the frontend joins it to /graph's geometry purely on
// segment_id (§R2). Only loaded segments are listed (load > 0) so a snapshot over
// a sparse network stays small; an absent segment reads as zero load by
// convention (matching the LoadSnapshot.Load contract).
type congestionResponse struct {
	// Segments is the loaded segments, sorted by segment_id for a stable,
	// deterministic body (Go map order is randomized; the snapshot must not be).
	Segments []segmentLoad `json:"segments"`
}

// segmentLoad is one segment's current load in vehicles/hour.
type segmentLoad struct {
	SegmentID string  `json:"segment_id"`
	LoadVPH   float64 `json:"load_vph"`
}

// handleCongestion serves GET /congestion: the current congestion snapshot,
// keyed by segment_id. It takes one owning Snapshot of the shared provider and
// renders the non-zero entries; the snapshot is a consistent point-in-time view,
// so the response is internally consistent even if the provider mutates later.
// The body is deterministic (segments sorted by segment_id) so a client diffing
// successive polls sees only genuine load changes, not reordering.
func (s *Server) handleCongestion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, endpointCongestion, http.StatusMethodNotAllowed, "method not allowed: use GET")
		return
	}

	snapshot := s.congestion.Snapshot()
	loads := make([]segmentLoad, 0)
	for edgeID, segment := range s.segmentByEdge {
		load := snapshot.Load(edgeID)
		if load <= 0 {
			continue
		}
		loads = append(loads, segmentLoad{SegmentID: string(segment), LoadVPH: load})
	}
	sort.Slice(loads, func(i, j int) bool { return loads[i].SegmentID < loads[j].SegmentID })

	s.writeJSON(w, endpointCongestion, http.StatusOK, congestionResponse{Segments: loads})
}
