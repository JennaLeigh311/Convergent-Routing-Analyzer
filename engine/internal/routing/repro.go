// repro.go holds the reproducibility scaffolding the Phase-3 iterative routers
// share: a single seed source for deterministic RNG, sorted node/edge iteration
// helpers (Go map iteration order is randomized, so any range-over-map on the
// assignment path silently breaks run-to-run determinism), and OD-set
// serialize/deserialize so a fixed seed plus a saved OD set reproduces a run
// byte-for-byte (the issue #71 determinism criterion).
package routing

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"strings"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// NewSeededRNG returns a *math/rand.Rand seeded from a single int64 seed — the one
// seed source the multipath/probabilistic-split router threads through every
// random choice so a run is reproducible from that seed alone. Using an explicit
// *rand.Rand (not the global, lazily-auto-seeded math/rand top-level functions)
// is deliberate: the global source's seed is not under the caller's control and is
// shared across goroutines, either of which would make a "fixed seed ⇒ identical
// output" guarantee impossible. A *rand.Rand is single-goroutine state; a
// concurrent Assign gives each worker its own seeded stream rather than sharing
// one.
//
// WARNING — per-worker seeding preserves thread-safety, NOT run-to-run
// determinism. Handing each worker its own seeded stream keeps concurrent Assign
// race-free, but if requests are sharded across workers nondeterministically (the
// usual case — work-stealing, arrival order, goroutine scheduling), a given
// request draws from a different stream from run to run, so the same fixed seed no
// longer reproduces the same output. A reproducible probabilistic router
// (multipath) must therefore seed PER REQUEST — derive each request's seed from
// its stable index/ID (e.g. NewSeededRNG(baseSeed ^ requestIndex)) — not per
// worker, so the draws a request makes depend only on the seed and the request,
// never on which worker happened to handle it.
func NewSeededRNG(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

// SortedNodeIDs returns every node id in roadGraph in ascending order. Use it
// instead of ranging a map of nodes anywhere on the assignment path: Go randomizes
// map iteration order, so a strategy that visited nodes in map order would place
// flow in a run-dependent order and break the determinism guarantee. The graph's
// NodeIDs are dense 0..NodeCount-1, so this is just that range materialized in
// order — but routing through this one helper keeps the "sorted, never map order"
// rule visible at the call site.
func SortedNodeIDs(roadGraph graph.Graph) []domain.NodeID {
	count := roadGraph.NodeCount()
	ids := make([]domain.NodeID, count)
	for index := 0; index < count; index++ {
		ids[index] = domain.NodeID(index)
	}
	return ids
}

// SortedEdgeIDs returns every edge id in roadGraph in ascending order, the dense
// 0..EdgeCount-1 range materialized in order. Same rationale as SortedNodeIDs:
// any per-edge accumulation that must be deterministic (e.g. summing a flow
// vector, or iterating edges to re-weight them between iterations) iterates this,
// never a map.
func SortedEdgeIDs(roadGraph graph.Graph) []domain.EdgeID {
	count := roadGraph.EdgeCount()
	ids := make([]domain.EdgeID, count)
	for index := 0; index < count; index++ {
		ids[index] = domain.EdgeID(index)
	}
	return ids
}

// WriteODSet serializes an OD set (a batch of RouteRequests) to w in a stable,
// line-oriented text form so a run can be reproduced byte-for-byte from a saved OD
// set plus a fixed seed (the issue #71 determinism criterion). The format is one
// request per line, tab-separated, in input order:
//
//	id<TAB>fromLat<TAB>fromLon<TAB>toLat<TAB>toLon<TAB>departAt<TAB>weight
//
// Coordinates and the float fields are written with strconv 'g'/-1 precision
// (shortest round-trippable decimal), so ReadODSet recovers the identical float64
// bit patterns and the replayed assignment is byte-identical. The id is written
// raw; it MUST NOT contain a tab, newline, or carriage return (a stray '\r' would
// otherwise survive into the last field on its line and silently corrupt the
// round-trip), which a request id never does. The fields are written in a fixed
// order, never from a map, so the serialization itself is deterministic.
func WriteODSet(w io.Writer, reqs []RouteRequest) error {
	buffered := bufio.NewWriter(w)
	for _, req := range reqs {
		if strings.ContainsAny(req.ID, "\t\n\r") {
			return fmt.Errorf("WriteODSet: request id %q contains a tab, newline, or carriage return, which the OD-set format reserves as delimiters", req.ID)
		}
		if _, err := fmt.Fprintf(buffered, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			req.ID,
			formatFloat(req.From.Lat), formatFloat(req.From.Lon),
			formatFloat(req.To.Lat), formatFloat(req.To.Lon),
			formatFloat(req.DepartAt), formatFloat(req.Weight),
		); err != nil {
			return fmt.Errorf("WriteODSet: %w", err)
		}
	}
	return buffered.Flush()
}

// ReadODSet parses an OD set written by WriteODSet back into a []RouteRequest, in
// the same input order, so WriteODSet→ReadODSet round-trips exactly (identical ids
// and identical float64 bit patterns). A line that does not have exactly seven
// tab-separated fields, or whose numeric fields do not parse, is a hard error — a
// malformed OD set must surface, not silently route a corrupted batch. Blank lines
// are skipped so a trailing newline is harmless.
func ReadODSet(r io.Reader) ([]RouteRequest, error) {
	scanner := bufio.NewScanner(r)
	// OD sets can be large (the 1,000-request demand batch); raise the line cap
	// well above bufio's 64KB default so a long line is not silently truncated.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var reqs []RouteRequest
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return nil, fmt.Errorf("ReadODSet: line %d: want 7 tab-separated fields, got %d", lineNo, len(fields))
		}
		fromLat, err := parseFloat(fields[1], lineNo, "fromLat")
		if err != nil {
			return nil, err
		}
		fromLon, err := parseFloat(fields[2], lineNo, "fromLon")
		if err != nil {
			return nil, err
		}
		toLat, err := parseFloat(fields[3], lineNo, "toLat")
		if err != nil {
			return nil, err
		}
		toLon, err := parseFloat(fields[4], lineNo, "toLon")
		if err != nil {
			return nil, err
		}
		departAt, err := parseFloat(fields[5], lineNo, "departAt")
		if err != nil {
			return nil, err
		}
		weight, err := parseFloat(fields[6], lineNo, "weight")
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, RouteRequest{
			ID:       fields[0],
			From:     domain.LatLon{Lat: fromLat, Lon: fromLon},
			To:       domain.LatLon{Lat: toLat, Lon: toLon},
			DepartAt: departAt,
			Weight:   weight,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ReadODSet: %w", err)
	}
	return reqs, nil
}

// formatFloat renders a float64 as the shortest decimal that round-trips back to
// the identical bit pattern (strconv 'g', precision -1), so WriteODSet→ReadODSet
// preserves the exact value a deterministic replay needs.
func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// parseFloat parses one OD-set numeric field, wrapping a parse failure with the
// line number and field name so a malformed OD set points at the offending value.
func parseFloat(field string, lineNo int, name string) (float64, error) {
	value, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, fmt.Errorf("ReadODSet: line %d: %s %q is not a number: %w", lineNo, name, field, err)
	}
	return value, nil
}
