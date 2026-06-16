// Package source builds a congestion.CongestionProvider from a small declarative
// Spec, so every entrypoint that needs a reactive congestion snapshot — the route
// CLI today, the future routing server (cmd/routing-server / internal/api) — shares
// ONE construction instead of copy-pasting the source switch.
//
// It deliberately lives BESIDE package congestion rather than inside it: the core
// congestion package keeps its doc-comment invariant that "the core depends only on
// this interface" and that "Concrete adapters live in subpackages ... so that import
// paths keep the engine core free of transport dependencies." Build imports the
// memory/static/simulator adapters, so it belongs in a sibling subpackage. The
// import direction stays acyclic: source -> {congestion, memory, static, simulator,
// domain, graph}, while the adapters depend only on congestion.
package source

import (
	"fmt"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/memory"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/simulator"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/static"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// SimSource is the Spec.Source value that selects the deterministic simulator
// provider (fixed seed). Any Spec.Source other than this and "" is treated as a
// file path to a §3 segment-congestion JSON batch. Callers that expose the source
// as a flag/config reference this so the literal lives in one place.
const SimSource = "sim"

// DefaultJamVPH is the default load Spec.JamVPH carries when a caller does not
// override it. It is set high enough to force the toy-graph divert: jamming the
// motorway corridor at this load lifts its BPR cost above the 1-hop residential
// edge's free-flow cost, so reactive reroutes. Override it for other graphs.
const DefaultJamVPH = 50000

// simulatorSeed is the FIXED seed for the SimSource provider. Pinning it makes the
// simulator-backed snapshot deterministic (project-spec.md §R5 reproducibility):
// the same jam injection yields the same snapshot, so a built provider does not
// vary run to run. Unexported because only Build consumes it.
const simulatorSeed int64 = 1

// Spec describes which congestion source to build and an optional jam injection.
// It is the shared, transport-free description both the CLI and the future server
// translate their flags/config into, so the provider construction has exactly one
// implementation (Build).
type Spec struct {
	// Source selects the provider: SimSource for the deterministic simulator, a
	// path to a §3 segment-congestion JSON batch (project-spec.md §3) for a static
	// provider, or "" for a zero-load in-memory provider.
	Source string
	// JamSegment is an optional segment_id to inject load onto so reactive diverts
	// around it (the Phase-2 divert demo); empty means no jam. It is honored only
	// by the SimSource and zero-load sources — a file Source already carries its
	// own loads, so combining the two is rejected as a contradiction.
	JamSegment string
	// JamVPH is the load (vehicles/hour) injected onto JamSegment; it is read only
	// when JamSegment != "".
	JamVPH float64
}

// Build constructs the frozen congestion snapshot the reactive router
// best-responds to (project-spec.md §R5) for spec over roadGraph. It chooses the
// source by spec.Source: SimSource is a fixed-seed simulator.New, a non-empty value
// is a file path decoded into a static.NewProvider (§3 batch), and "" is a zero-load
// memory.New. When spec.JamSegment is non-empty its load (spec.JamVPH) is injected
// onto the resolved edge so reactive diverts around it; the sim and zero-load sources
// support injection, while a file source already carries its own loads (a jam
// alongside a file source is rejected as a contradiction rather than silently
// dropped). The graph sizes the dense per-edge provider and resolves a jam
// segment_id to its EdgeID.
//
// An unknown jam segment_id is a clean caller error naming the bad segment, not a
// silent no-op, so a typo cannot quietly produce the un-diverted route.
func Build(roadGraph graph.Graph, spec Spec) (congestion.CongestionProvider, error) {
	switch spec.Source {
	case SimSource:
		sim := simulator.New(simulatorSeed, roadGraph.EdgeCount())
		if spec.JamSegment != "" {
			edgeID, err := resolveJamSegment(roadGraph, spec.JamSegment)
			if err != nil {
				return nil, err
			}
			sim.Inject(edgeID, spec.JamVPH)
		}
		return sim, nil
	case "":
		mem := memory.New(roadGraph.EdgeCount())
		if spec.JamSegment != "" {
			edgeID, err := resolveJamSegment(roadGraph, spec.JamSegment)
			if err != nil {
				return nil, err
			}
			mem.Set(edgeID, spec.JamVPH)
		}
		return mem, nil
	default:
		if spec.JamSegment != "" {
			return nil, fmt.Errorf("cannot combine -jam with a file -congestion %q (the file already carries the loads); use -congestion sim to inject", spec.Source)
		}
		messages, err := domain.DecodeSegmentCongestionFile(spec.Source)
		if err != nil {
			return nil, fmt.Errorf("load congestion %q: %w", spec.Source, err)
		}
		index := static.BuildSegmentEdgeIndex(roadGraph)
		provider, err := static.NewProvider(messages, index, roadGraph.EdgeCount())
		if err != nil {
			return nil, fmt.Errorf("load congestion %q: %w", spec.Source, err)
		}
		return provider, nil
	}
}

// resolveJamSegment maps a jam segment_id to its dense EdgeID via the graph's
// segment->edge index (static.BuildSegmentEdgeIndex). An unknown segment_id is a
// caller error that NAMES the bad segment, so a typo fails loudly rather than
// no-opping into the un-jammed (un-diverted) route.
func resolveJamSegment(roadGraph graph.Graph, jamSegment string) (domain.EdgeID, error) {
	index := static.BuildSegmentEdgeIndex(roadGraph)
	edgeID, found := index[domain.SegmentID(jamSegment)]
	if !found {
		return 0, fmt.Errorf("invalid -jam %q: no such segment_id in the graph", jamSegment)
	}
	return edgeID, nil
}
