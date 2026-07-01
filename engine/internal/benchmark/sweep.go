package benchmark

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/static"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// SweepDemandVPH is the FIXED total demand (vehicles/hour, summed over all R
// requests) the whole sweep routes at every level. Holding total demand fixed and
// varying ONLY cost.BPR.CapacityScale is the honest demand-sweep design: the
// project-spec.md §R3 capacity knob IS the sweep axis, so each level is the same
// reproducible OD set seen against a different effective capacity. The value 5000
// is chosen empirically so that at CapacityScale = 1.0 the busiest reference
// corridor on the toy graph realizes v/c ≈ 1.0 (the saturated level), which anchors
// the four scales below — see SweepLevels.
const SweepDemandVPH = 5000.0

// SweepLevel is one demand-sweep cell's definition: a human label, the TARGET v/c
// band it drives the busiest corridor toward, and the cost.BPR.CapacityScale that
// achieves it on the toy graph. CapacityScale is the only thing that varies across
// the sweep (total demand is fixed at SweepDemandVPH), so a level is fully
// described by its scale; the realized v/c is reported per cell in the table
// (Result.MaxVC) alongside this target, because the target is a calibration intent,
// not a guarantee on a sparse hand-built network.
type SweepLevel struct {
	// Label is the column identity carried into Result.DemandLevel and the OD set.
	Label string
	// TargetVC is the volume/capacity band this level was calibrated to drive the
	// busiest reference corridor toward.
	TargetVC float64
	// CapacityScale is the cost.BPR.CapacityScale (project-spec.md §R3) that, at the
	// fixed SweepDemandVPH, drives the busiest corridor to ≈ TargetVC. v/c scales
	// INVERSELY with CapacityScale (effective capacity = CapacityVPH × scale), so a
	// SMALLER scale ⇒ a HIGHER v/c.
	CapacityScale float64
}

// SweepLevels is the canonical four-level demand sweep (project-spec.md §R5):
// v/c ∈ {0.5, 0.8, 1.0, 1.2}, achieved by varying ONLY cost.BPR.CapacityScale at a
// fixed SweepDemandVPH total demand. The scales are calibrated empirically against
// the toy graph's busiest corridor:
//
//	scale 2.00 ⇒ maxVC ≈ 0.50   (free flow / light)
//	scale 1.25 ⇒ maxVC ≈ 0.80   (moderate)
//	scale 1.00 ⇒ maxVC ≈ 1.00   (saturated)
//	scale 0.84 ⇒ maxVC ≈ 1.20   (over-capacity)
//
// The mapping is honest about HOW the target v/c is achieved: total demand is held
// fixed and the §R3 capacity knob is swept, so each level is the SAME reproducible
// OD set seen against a different effective capacity. The realized v/c (MaxVC) is
// reported per cell so the small calibration drift from the targets is visible, not
// hidden.
//
// IMPORTANT (project-spec.md §5, docs/benchmarks.md): on the hand-built toy graph
// the Price of Anarchy is ≈ 1 at EVERY level — the toy network has no Braess/Pigou
// structure, so naive and systemoptimal land on the same split and there is no
// gap to close. That is the truthful result on this fixture, NOT a bug. The strict
// PoA > 1 demonstration lives on testdata/toy_network_pigou.geojson (issue #89);
// this sweep is the six-router comparison harness CI Lane A runs on the toy graph,
// and it reports the honest PoA ≈ 1 across all four levels rather than quoting a
// cherry-picked figure (PoA peaks at moderate load and → 1 at both extremes).
func SweepLevels() []SweepLevel {
	return []SweepLevel{
		{Label: "vc0.5", TargetVC: 0.5, CapacityScale: 2.00},
		{Label: "vc0.8", TargetVC: 0.8, CapacityScale: 1.25},
		{Label: "vc1.0", TargetVC: 1.0, CapacityScale: 1.00},
		{Label: "vc1.2", TargetVC: 1.2, CapacityScale: 0.84},
	}
}

// RouterOrder is the canonical ordering of the six routers in the comparison table.
// Held in one place so the table, the CI assertions, and the determinism test all
// agree on which routers must appear and in what order.
var RouterOrder = []string{"naive", "reactive", "incremental", "msa", "systemoptimal", "multipath"}

// SweepCell is one (router, demand level) cell of the comparison table: the
// realized-time Result plus the cross-router relationships a lone Result cannot
// hold — the Price of Anarchy of this cell's router against the level's
// systemoptimal reference, the experienced-over-the-run simulator-mode realized
// times, and the CapacityScale the level ran under. It is the table's row type; it
// is JSON-serializable for the saved artifact and Markdown-renderable for docs.
//
// PoA lives here (not on Result) for the reason result.go documents: PoA is a
// relationship BETWEEN two results on the same OD set, not a property of one cell.
// The sweep computes it once per cell (selfish = naive of the same level, reference
// = systemoptimal of the same level) and attaches it so the table need not
// re-divide.
type SweepCell struct {
	// Result is the realized-time metric bundle for this cell (router × level).
	Result Result `json:"result"`

	// CapacityScale is the cost.BPR.CapacityScale this level ran under — the sweep
	// axis, recorded per cell so the JSON artifact is self-describing.
	CapacityScale float64 `json:"capacity_scale"`

	// TargetVC is the level's calibration-target v/c (Result.MaxVC reports the
	// realized one), kept so the table can show target-vs-realized side by side.
	TargetVC float64 `json:"target_vc"`

	// PoA is the realized-time Price of Anarchy of this cell's router against the
	// SAME level's systemoptimal total: TotalNetworkTime(this) /
	// TotalNetworkTime(systemoptimal at this level). For the systemoptimal cell
	// itself this is 1 by construction; for naive it is the headline ratio. It is
	// computed via PriceOfAnarchy, so a degenerate total yields 1, never NaN/Inf.
	PoA float64 `json:"poa"`

	// SimMeanRealizedS / SimP95RealizedS are the EXPERIENCED (over-the-run) mean and
	// p95 realized travel times (seconds) from the §R5 discrete-time mesoscopic
	// simulator (benchmark.Simulate): this level's OD set is released on a sim clock
	// and each vehicle advances along its routed path at the BPR-derived edge speed,
	// the per-edge load re-deriving each tick from who is on each edge, so congestion
	// builds and drains over the run. They are the simulator-mode columns, read beside
	// the static Result.MeanRealizedS / P95RealizedS.
	//
	// They are DELIBERATELY DISTINCT from the static-equilibrium numbers (the §R5
	// honesty distinction): the static number assumes every request is simultaneously
	// present at the converged flow; these are the times vehicles actually experienced
	// as the peak filled and drained. The run is deterministic — a fixed StartTime,
	// TickSeconds, and MaxTicks over the reproducible OD set — so two sweeps yield
	// byte-identical sim columns. The router driving the sim is the congestion-aware
	// ReactiveBPRFactory, so newly-released requests best-respond to the load built up
	// so far; it is the same single-shot reactive code path the static reactive cell
	// uses, not an iterative router (the simulator only exercises router.Route).
	SimMeanRealizedS float64 `json:"sim_mean_realized_s"`
	SimP95RealizedS  float64 `json:"sim_p95_realized_s"`
}

// simStartTime is the fixed sim-clock origin (the SimConfig.StartTime) the
// simulator-mode columns run under. Pinned (not clock-derived) so the run is
// byte-identical run to run — the §R5 determinism criterion. The instant itself only
// labels tick 0; the relative dynamics (and therefore the experienced times) are
// unaffected by which instant it is, so any fixed value serves. It is the Phase-5
// time-of-day origin the frontend would later set; the sweep pins it.
var simStartTime = time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)

// simTickSeconds is Δt for the simulator-mode columns (§R5 "Δt≈30s"). Fixed so the
// run is reproducible; the simulator defaults to the same value when unset, but the
// sweep pins it explicitly so the determinism contract does not depend on a default.
const simTickSeconds = 30.0

// simMaxTicks bounds each simulator-mode run. The OD set departs all at t=0 (the
// static all-at-once batch RouteRequests produces), so the run is a single burst that
// drains via the simulator's drained-network early exit well before this cap; the cap
// only guarantees a degenerate route can never spin forever. It is generous so a
// healthy run always finishes by the early exit, keeping Completed == Released.
const simMaxTicks = 10000

// RunSweep runs ALL SIX routers over the SAME reproducible OD set at every demand
// level and returns the full comparison grid, one SweepCell per (router, level), in
// (level, RouterOrder) order. It is the harness's core: the CLI renders the
// returned cells as JSON and a Markdown table, and the determinism test asserts two
// RunSweep calls produce identical cells (the metrics carry no wall-clock).
//
// For each level it (1) generates the level's OD set with the fixed seed (so every
// router at that level sees the identical demand), (2) builds the level's BPR from
// its CapacityScale, (3) routes the OD set through each of the six routers, (4)
// evaluates each into a Result, (5) computes PoA against the level's systemoptimal
// total and runs the §R5 mesoscopic simulator once for the level's experienced
// (over-the-run) mean/p95 realized time. A routing error from any router is
// returned (never a partial grid) so CI fails the build rather than publishing a
// half table. count is the per-level request count (DefaultODCount for the
// canonical run).
//
// Unreachable OD pairs cannot occur here: GenerateODSet draws only from the graph's
// reachable-pair pool, so every synthesized request routes cleanly. An empty pool
// (a fully disconnected graph) yields an empty OD set and finite, zero Results —
// never a panic or NaN.
func RunSweep(ctx context.Context, g graph.Graph, seed int64, count int) ([]SweepCell, error) {
	levels := SweepLevels()
	cells := make([]SweepCell, 0, len(levels)*len(RouterOrder))

	for _, level := range levels {
		set := GenerateODSet(g, seed, count, level.Label, level.TargetVC, SweepDemandVPH)
		reqs := set.RouteRequests()
		bpr := cost.NewBPR(0.15, 4, level.CapacityScale)

		// Route every router at this level, collecting the per-router AssignResult
		// keyed by name so PoA can divide against systemoptimal afterward — through the
		// shared static-assignment seam (assignStatic).
		results, err := assignStatic(ctx, g, bpr, reqs, seed, RouterOrder, fmt.Sprintf("sweep: level %q", level.Label))
		if err != nil {
			return nil, err
		}

		referenceTotal := routing.TotalNetworkTime(g, bpr, results["systemoptimal"].FinalFlows)

		// Run the §R5 mesoscopic simulator ONCE for this level: it releases the level's
		// OD set on the sim clock and best-responds to the congestion that builds, under
		// the congestion-aware ReactiveBPRFactory. The experienced (over-the-run) mean/p95
		// it returns characterize the LEVEL (a single time-domain sanity check beside the
		// static one-shot), so they are attached identically to every router cell of the
		// level — the simulator's router is the factory, not the cell's static router.
		sim, err := simLevel(ctx, g, reqs, bpr)
		if err != nil {
			return nil, fmt.Errorf("sweep: level %q simulate: %w", level.Label, err)
		}

		for _, name := range RouterOrder {
			res := results[name]
			result := Evaluate(g, bpr, name, level.Label, res)
			cells = append(cells, SweepCell{
				Result:           result,
				CapacityScale:    level.CapacityScale,
				TargetVC:         level.TargetVC,
				PoA:              PriceOfAnarchy(result.TotalNetworkTimeS, referenceTotal),
				SimMeanRealizedS: sim.MeanRealizedS,
				SimP95RealizedS:  sim.P95RealizedS,
			})
		}
	}
	return cells, nil
}

// simLevel runs the §R5 mesoscopic simulator ONCE over a level's OD set under the
// PINNED sim clock (simStartTime / simTickSeconds / simMaxTicks) and the given BPR,
// driven by the congestion-aware ReactiveBPRFactory. Both RunSweep and RunSingle call
// it so the simulator-mode columns are wired identically in one place — a change to the
// SimConfig, the driving factory, or the observer lands here once rather than drifting
// across two parallel blocks. The experienced (over-the-run) mean/p95 it returns
// characterize the LEVEL and are attached to every router cell of that level. The caller
// wraps the error with its own (sweep-level vs single) context.
func simLevel(ctx context.Context, g graph.Graph, reqs []routing.RouteRequest, bpr cost.BPR) (SimResult, error) {
	return Simulate(ctx, g, reqs, SimConfig{
		StartTime:   simStartTime,
		TickSeconds: simTickSeconds,
		MaxTicks:    simMaxTicks,
		BPR:         bpr,
	}, ReactiveBPRFactory(g, bpr), nil)
}

// singleLevelLabel is the demand-level column identity a single-algorithm run
// (RunSingle) carries. The four-level sweep labels each cell by its target v/c band
// (vc0.5 … vc1.2); a single run pins capacity_scale to the client's value rather
// than sweeping it, so there is no band to name — "single" makes the one synthesized
// level self-describing in the Result/OD set, and RunSingle reports the realized v/c
// (Result.MaxVC) so the actual utilization is still visible per cell.
const singleLevelLabel = "single"

// singleTargetVC is the OD-generator target-v/c label a single-algorithm run tags
// its OD set and cell with. It is METADATA ONLY: GenerateODSet uses targetVC purely
// as a recorded label (the realized load is set by SweepDemandVPH spread evenly over
// the requests and the client's capacity_scale, NOT by targetVC), so its value does
// not change which requests are drawn or what they realize. We pin it to 1.0 — the
// saturated reference band — as an honest neutral default: it documents "this run was
// not calibrated to a particular v/c band" rather than implying a calibration RunSingle
// does not perform. The realized v/c the client actually sees is Result.MaxVC, driven
// by their capacity_scale.
const singleTargetVC = 1.0

// RunSingle runs ONE named router (plus the systemoptimal reference, so the reported
// Price of Anarchy is honest) over a single reproducible OD set, under a cost.BPR
// built FROM THE CLIENT'S α/β/capacity_scale. It is the single-algorithm benchmark
// mode behind POST /benchmark when `algorithm` names a specific router (one of
// RouterOrder) rather than the default "all": unlike RunSweep — whose axis IS
// capacity_scale, swept across four levels — RunSingle PINS capacity_scale to the
// caller's value and runs exactly one level, so α/β/capacity_scale actually flow into
// the cost function and two runs differing only in those params return different
// metrics (the §R6 params are no longer inert).
//
// It returns the named router's SweepCell followed, when the named router is NOT
// systemoptimal, by the systemoptimal reference cell — both at the synthesized
// "single" level. The reference cell is included (rather than discarded after the PoA
// divide) so the returned grid is self-describing and BuildReport's per-level helpers
// see the same naive/systemoptimal shape they do for the sweep. When the named router
// IS systemoptimal, only one cell is returned and its PoA is 1 by construction (it is
// its own reference). The named router's cell is ALWAYS present and first.
//
// Determinism matches RunSweep: the OD set is drawn with the fixed seed, the BPR is
// built from fixed params, and the §R5 simulator runs under the same pinned clock, so
// two RunSingle calls with identical arguments return byte-identical cells. A routing
// error from either router is returned (never a partial grid). name must be one of
// RouterOrder; an unknown name is a clean error (the API rejects it as a 400 before
// reaching here, but RunSingle guards it too rather than trusting the caller).
func RunSingle(ctx context.Context, g graph.Graph, seed int64, count int, alpha, beta, capacityScale float64, name string) ([]SweepCell, error) {
	if _, ok := routerIndex[name]; !ok {
		return nil, fmt.Errorf("benchmark: RunSingle unknown router %q", name)
	}

	// ONE BPR from the client's params — the whole point of single mode: α/β and the
	// pinned capacity_scale drive the cost curve, so the metrics reflect the request.
	bpr := cost.NewBPR(alpha, beta, capacityScale)

	set := GenerateODSet(g, seed, count, singleLevelLabel, singleTargetVC, SweepDemandVPH)
	reqs := set.RouteRequests()

	// The named router plus systemoptimal (the PoA reference). Deduped: if the named
	// router IS systemoptimal we route it once and PoA is 1 by construction.
	names := []string{name}
	if name != "systemoptimal" {
		names = append(names, "systemoptimal")
	}

	// The same shared static-assignment seam RunSweep uses (assignStatic).
	results, err := assignStatic(ctx, g, bpr, reqs, seed, names, "single:")
	if err != nil {
		return nil, err
	}

	referenceTotal := routing.TotalNetworkTime(g, bpr, results["systemoptimal"].FinalFlows)

	// Run the §R5 mesoscopic simulator ONCE for the level (same pinned clock/config as
	// RunSweep, via the shared simLevel helper) so the experienced-over-the-run columns
	// are populated and the cells are shaped exactly like a sweep cell.
	sim, err := simLevel(ctx, g, reqs, bpr)
	if err != nil {
		return nil, fmt.Errorf("single: simulate: %w", err)
	}

	cells := make([]SweepCell, 0, len(names))
	for _, n := range names {
		result := Evaluate(g, bpr, n, singleLevelLabel, results[n])
		cells = append(cells, SweepCell{
			Result:           result,
			CapacityScale:    capacityScale,
			TargetVC:         singleTargetVC,
			PoA:              PriceOfAnarchy(result.TotalNetworkTimeS, referenceTotal),
			SimMeanRealizedS: sim.MeanRealizedS,
			SimP95RealizedS:  sim.P95RealizedS,
		})
	}
	return cells, nil
}

// routerIndex is the membership set of canonical router names (RouterOrder), built
// once so RunSingle and any caller can validate a router name in O(1) without
// re-scanning the slice. It is the single source of "is this a known router".
var routerIndex = func() map[string]struct{} {
	m := make(map[string]struct{}, len(RouterOrder))
	for _, n := range RouterOrder {
		m[n] = struct{}{}
	}
	return m
}()

// IsRouter reports whether name is one of the six canonical routers (RouterOrder).
// The API's benchmark validation uses it to reject an unknown `algorithm` with a
// clean 400 (alongside the "all" default it accepts separately), so the membership
// rule lives in one place beside RouterOrder rather than being re-listed in the API.
func IsRouter(name string) bool {
	_, ok := routerIndex[name]
	return ok
}

// buildRouter constructs the named router over the graph with the given BPR. It is
// the one place the six constructors' differing signatures are reconciled, so both
// RunSweep and the §R6 parallel run (parallel.go) iterate RouterOrder uniformly
// through a single builder — a new router or a constructor-signature change lands here
// once, not in two parallel switches.
//
// The reactive router needs a congestion.CongestionProvider, supplied by the caller:
// the sweep passes a frozen all-zero-load snapshot (static.NewFromSnapshot(nil)) so
// reactive best-responds to a free-flow snapshot — honest for a static sweep with no
// externally-injected congestion — while the mesoscopic parallel run passes the LIVE
// per-tick provider so reactive best-responds to the congestion built up so far. The
// other five routers ignore the provider. multipath is seeded with the fixed seed and
// sweepK paths so its probabilistic split is reproducible.
func buildRouter(name string, g graph.Graph, bpr cost.BPR, provider congestion.CongestionProvider, seed int64) (routing.Router, error) {
	switch name {
	case "naive":
		return routing.NewNaiveRouter(g), nil
	case "reactive":
		return routing.NewReactiveRouter(g, bpr, provider), nil
	case "incremental":
		return routing.NewIncrementalRouter(g, bpr), nil
	case "msa":
		return routing.NewMSARouter(g, bpr), nil
	case "systemoptimal":
		return routing.NewSystemOptimalRouter(g, bpr), nil
	case "multipath":
		return routing.NewMultipathRouter(g, bpr, seed, sweepK), nil
	default:
		return nil, fmt.Errorf("benchmark: unknown router %q", name)
	}
}

// sweepK is the per-request K-shortest path count for the multipath router in the
// sweep, matching cmd/benchmark's benchK: small enough to be cheap on the sparse
// toy graph, where some OD pairs have fewer than K simple paths (the split then
// collapses onto the one available path, which is fine).
const sweepK = 3

// assignStatic runs each named router's real iterative AssignResult over the shared
// OD set under a frozen free-flow reference snapshot (static.NewFromSnapshot(nil)),
// returning the per-router AssignResult keyed by name. It is the ONE place the static
// assignment orchestration lives — shared by RunSweep, RunSingle, and the live PoA
// reference (staticPoARef in parallel.go) — so the "build with the free-flow snapshot,
// run AssignResult" seam a future Spark congestion feed replaces exists once, not in
// three copies. The reactive router reads the nil snapshot as free-flow through the
// shared static.Provider port; the other five ignore the provider. A routing error is
// wrapped with errContext (a caller-supplied prefix, e.g. `sweep: level "vc0.5"`) and
// the offending router name, and returned with a nil map — never a partial result.
// It is deterministic: a fixed OD set yields byte-identical FinalFlows per router.
func assignStatic(ctx context.Context, g graph.Graph, bpr cost.BPR, reqs []routing.RouteRequest, seed int64, names []string, errContext string) (map[string]routing.AssignResult, error) {
	results := make(map[string]routing.AssignResult, len(names))
	for _, name := range names {
		router, err := buildRouter(name, g, bpr, static.NewFromSnapshot(nil), seed)
		if err != nil {
			return nil, err
		}
		res, err := router.AssignResult(ctx, reqs)
		if err != nil {
			return nil, fmt.Errorf("%s router %q: %w", errContext, name, err)
		}
		results[name] = res
	}
	return results, nil
}

// sweepMarkdownColumns is the comparison table's column order: the Result columns
// plus the sweep-specific cross-router columns (target/realized v/c, PoA, the
// simulator-mode total, and the CapacityScale axis). Held separate from
// result.go's markdownColumns because the sweep table is a SUPERSET — it adds the
// relationships a lone Result cannot carry.
var sweepMarkdownColumns = []string{
	"router", "demand", "cap_scale", "target_vc", "max_vc",
	"mean_s", "p95_s", "total_s", "poa", "sim_mean_s", "sim_p95_s", "gini_vc", "iters", "converged", "gap",
}

// SweepMarkdownHeader returns the comparison table's header row (no trailing
// newline). Pair with SweepMarkdownSeparator and one SweepCell.MarkdownRow per cell.
func SweepMarkdownHeader() string {
	return "| " + strings.Join(sweepMarkdownColumns, " | ") + " |"
}

// SweepMarkdownSeparator returns the header/body separator matching the column
// count.
func SweepMarkdownSeparator() string {
	dashes := make([]string, len(sweepMarkdownColumns))
	for i := range dashes {
		dashes[i] = "---"
	}
	return "| " + strings.Join(dashes, " | ") + " |"
}

// MarkdownRow renders this SweepCell as one comparison-table row, in
// SweepMarkdownHeader column order. Floats use fixed precision (2 decimals for
// times/ratios, 4 for v/c, %g for the wide-range gap) so the rendered table is
// byte-stable for a given cell — the determinism criterion at the render layer.
func (c SweepCell) MarkdownRow() string {
	r := c.Result
	cells := []string{
		r.Router,
		r.DemandLevel,
		fmt.Sprintf("%.2f", c.CapacityScale),
		fmt.Sprintf("%.2f", c.TargetVC),
		fmt.Sprintf("%.4f", r.MaxVC),
		fmt.Sprintf("%.2f", r.MeanRealizedS),
		fmt.Sprintf("%.2f", r.P95RealizedS),
		fmt.Sprintf("%.2f", r.TotalNetworkTimeS),
		fmt.Sprintf("%.4f", c.PoA),
		fmt.Sprintf("%.2f", c.SimMeanRealizedS),
		fmt.Sprintf("%.2f", c.SimP95RealizedS),
		fmt.Sprintf("%.4f", r.GiniVC),
		fmt.Sprintf("%d", r.Iters),
		fmt.Sprintf("%t", r.Converged),
		fmt.Sprintf("%g", r.Gap),
	}
	return "| " + strings.Join(cells, " | ") + " |"
}

// RenderMarkdownTable assembles the full GitHub-flavored Markdown table for the
// sweep grid: header, separator, then one row per cell in the order RunSweep
// returned them ((level, RouterOrder) order). It returns the table body with a
// trailing newline so it drops straight into a docs section.
func RenderMarkdownTable(cells []SweepCell) string {
	var b strings.Builder
	b.WriteString(SweepMarkdownHeader())
	b.WriteByte('\n')
	b.WriteString(SweepMarkdownSeparator())
	b.WriteByte('\n')
	for _, c := range cells {
		b.WriteString(c.MarkdownRow())
		b.WriteByte('\n')
	}
	return b.String()
}

// Improvement is the headline improvement number WITH the demand level it was
// measured at (project-spec.md §5: "quote the improvement % with its demand
// level", never a cherry-picked figure). It is the percentage by which the BEST
// coordinated router (the lower realized total of incremental / systemoptimal)
// reduces the realized total network time versus the naive selfish baseline, AT THE
// LEVEL WHERE THAT REDUCTION IS LARGEST.
type Improvement struct {
	// DemandLevel is the level (and its target v/c) where the improvement is
	// largest — attached because the improvement is meaningless without it (PoA
	// peaks at moderate load and → 1 at the extremes).
	DemandLevel string  `json:"demand_level"`
	TargetVC    float64 `json:"target_vc"`

	// BestRouter is which coordinated router (incremental or systemoptimal)
	// achieved the best realized total at that level.
	BestRouter string `json:"best_router"`

	// PercentReduction is 100 × (naiveTotal − bestTotal) / naiveTotal at that
	// level: how much realized total network time the best coordinated router saves
	// versus selfish naive. On the toy graph this is ≈ 0 (no Braess/Pigou gap), and
	// that honest zero is reported rather than hidden.
	PercentReduction float64 `json:"percent_reduction"`

	// NaiveTotalS / BestTotalS are the two realized totals the reduction is computed
	// from, kept so the figure is auditable.
	NaiveTotalS float64 `json:"naive_total_s"`
	BestTotalS  float64 `json:"best_total_s"`
}

// HeadlineImprovement scans the sweep grid for the level at which the best
// coordinated router (the lower realized total of incremental / systemoptimal)
// most reduces realized total network time versus naive, and returns that
// improvement WITH its demand level. Improvement = naive vs. the best of
// incremental / systemoptimal, exactly as the issue specifies.
//
// It picks the MAXIMUM reduction across levels (not a fixed level) because the
// improvement peaks at moderate load and shrinks to ≈ 0 at both extremes; reporting
// the peak with its level is the honest single figure. Ties and the toy graph's
// uniform PoA ≈ 1 resolve deterministically: levels are scanned in SweepLevels
// order and the first level achieving the max reduction wins, so two runs report
// the same level. If the grid is empty (no cells) it returns a zero Improvement.
func HeadlineImprovement(cells []SweepCell) Improvement {
	// Index cells by (level, router) total.
	type key struct{ level, router string }
	totals := make(map[key]float64, len(cells))
	levelOrder := make([]string, 0)
	seenLevel := make(map[string]bool)
	targetVC := make(map[string]float64)
	for _, c := range cells {
		totals[key{c.Result.DemandLevel, c.Result.Router}] = c.Result.TotalNetworkTimeS
		if !seenLevel[c.Result.DemandLevel] {
			seenLevel[c.Result.DemandLevel] = true
			levelOrder = append(levelOrder, c.Result.DemandLevel)
		}
		targetVC[c.Result.DemandLevel] = c.TargetVC
	}

	var best Improvement
	found := false
	for _, level := range levelOrder {
		naive, okN := totals[key{level, "naive"}]
		inc, okI := totals[key{level, "incremental"}]
		so, okS := totals[key{level, "systemoptimal"}]
		if !okN {
			continue
		}
		// Best coordinated total = lower of incremental / systemoptimal that exist.
		bestRouter := ""
		bestTotal := 0.0
		if okI {
			bestRouter, bestTotal = "incremental", inc
		}
		if okS && (bestRouter == "" || so < bestTotal) {
			bestRouter, bestTotal = "systemoptimal", so
		}
		if bestRouter == "" {
			continue
		}
		var pct float64
		if naive > 0 {
			pct = 100 * (naive - bestTotal) / naive
		}
		if !found || pct > best.PercentReduction {
			best = Improvement{
				DemandLevel:      level,
				TargetVC:         targetVC[level],
				BestRouter:       bestRouter,
				PercentReduction: pct,
				NaiveTotalS:      naive,
				BestTotalS:       bestTotal,
			}
			found = true
		}
	}
	return best
}

// PoAByLevel returns the naive-vs-systemoptimal Price of Anarchy at each demand
// level in SweepLevels order — the per-level PoA the issue requires reported across
// ALL four sweep levels (never a single figure). Each entry is the naive cell's PoA
// at that level (which is, by RunSweep's construction, naiveTotal / soTotal).
func PoAByLevel(cells []SweepCell) []LevelPoA {
	out := make([]LevelPoA, 0)
	seen := make(map[string]bool)
	for _, c := range cells {
		if c.Result.Router != "naive" {
			continue
		}
		if seen[c.Result.DemandLevel] {
			continue
		}
		seen[c.Result.DemandLevel] = true
		out = append(out, LevelPoA{
			DemandLevel: c.Result.DemandLevel,
			TargetVC:    c.TargetVC,
			PoA:         c.PoA,
		})
	}
	// RunSweep already emits in level order; sort defensively by target v/c so the
	// list is stable even if cells are reordered upstream.
	sort.SliceStable(out, func(i, j int) bool { return out[i].TargetVC < out[j].TargetVC })
	return out
}

// LevelPoA is one demand level's headline Price of Anarchy (naive vs systemoptimal),
// with the level label and its target v/c for context.
type LevelPoA struct {
	DemandLevel string  `json:"demand_level"`
	TargetVC    float64 `json:"target_vc"`
	PoA         float64 `json:"poa"`
}
