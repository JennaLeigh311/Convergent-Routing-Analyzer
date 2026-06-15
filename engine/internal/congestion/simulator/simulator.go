// Package simulator holds the synthetic congestion.CongestionProvider adapter:
// it injects and evolves per-edge load (vehicles/hour) over discrete ticks so
// the routing brain sees congestion build and routes visibly change, with no
// data plane present. It is the local-dev/test congestion source that exercises
// the engine before Kafka/Spark exist (project-spec.md §2: "Local development
// and unit tests plug in the static/simulator adapter — the entire routing brain
// is developed against the simulator").
//
// This is the simple injector, NOT the §R5 mesoscopic benchmark simulator (no
// departure times, no Δt continuous stepping, no per-vehicle dynamics) — that
// ships in Phase 4. The evolution model here is intentionally simple and not
// physically realistic; its only contract is to be deterministic per seed.
//
// Reproducibility (project-spec.md §R5: "fixed RNG seed ... seeded split"): all
// randomness flows through a single seeded *math/rand.Rand, so two simulators
// built with the same seed and driven by the same Inject + Step sequence produce
// an identical load history, tick by tick. This determinism is what lets tests
// and demand sweeps replay an evolving congestion scenario exactly.
//
// The adapter lives in this subpackage — not in the congestion core — so the
// core package keeps no dependency on any concrete source, per the
// CongestionProvider port's doc comment.
package simulator

import (
	"math/rand"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// perturbationFraction bounds the per-tick random change Step applies to a
// loaded edge: each tick multiplies an edge's load by a factor drawn uniformly
// from [1-perturbationFraction, 1+perturbationFraction]. It is a simple,
// documented evolution knob — large enough that loads visibly change across
// ticks (so routes shift) but bounded so a corridor that was jammed stays
// roughly jammed for several ticks rather than swinging wildly.
const perturbationFraction = 0.10

// Simulator is a synthetic CongestionProvider. It holds a congestion.LoadStore
// (a dense per-edge load slice, vehicles/hour) and a seeded *math/rand.Rand;
// Inject raises the load on specific edges (jam a corridor) and Step evolves the
// loaded edges by one discrete tick using the RNG, so the evolution is
// reproducible per seed (project-spec.md §R5).
//
// It is safe for a single writer; concurrent Inject/Step/Snapshot calls are NOT
// synchronized and must be serialized by the caller. An EdgeID outside the
// backing store (or never injected) reads as 0, matching the LoadSnapshot.Load
// contract for an unobserved edge.
type Simulator struct {
	// store is the dense per-edge load backing slice; Snapshot returns a fresh
	// copy of it, never the live store, so a caller can mutate a snapshot without
	// disturbing the simulator.
	store *congestion.LoadStore
	// rng is the single seeded source of all randomness. Routing every random
	// draw through it is what guarantees that the same seed plus the same
	// Inject/Step sequence reproduces an identical load history (§R5).
	rng *rand.Rand
}

// New returns a Simulator sized to hold edges 0..edgeCount-1, with every edge
// initially unloaded (load 0) and a *math/rand.Rand seeded by seed. The same
// seed yields an identical RNG stream, so two simulators built with the same
// seed and driven by the same Inject + Step sequence evolve identically
// (project-spec.md §R5 reproducibility). edgeCount is typically the graph's
// EdgeCount so every valid EdgeID indexes the dense backing store without a
// grow; a negative edgeCount is treated as zero (Inject still grows on demand).
func New(seed int64, edgeCount int) *Simulator {
	return &Simulator{
		store: congestion.NewLoadStore(edgeCount),
		rng:   rand.New(rand.NewSource(seed)),
	}
}

// Inject ADDS loadVPH to the current load on a single edge (additive semantics),
// growing the backing store if edgeID is beyond the current capacity so a
// simulator built smaller than the graph still accepts later edges. Additive
// injection is how a caller stacks demand onto a corridor; the acceptance
// criterion ("injecting load on edge e makes Load(e) exceed its zero baseline")
// holds because the baseline is 0 and loadVPH is added to it. A negative edgeID
// is ignored (it can never be a valid dense EdgeID and would read back as 0
// anyway); the resulting load is floored at 0 so a negative loadVPH can never
// drive an edge below zero and break the router's edge-weight non-negativity
// assumption.
func (simulator *Simulator) Inject(edgeID domain.EdgeID, loadVPH float64) {
	simulator.store.Set(edgeID, nonNegative(simulator.store.Load(edgeID)+loadVPH))
}

// InjectAll adds load to many edges in one call, the bulk counterpart to Inject
// (e.g. jam a whole corridor at once). It grows the backing store once to fit
// the largest EdgeID rather than re-growing per edge. Negative EdgeIDs are
// skipped. Semantics match Inject: each entry's value is ADDED to that edge's
// current load and the result is floored at 0.
func (simulator *Simulator) InjectAll(loads map[domain.EdgeID]float64) {
	maxIndex := 0
	for edgeID := range loads {
		if edgeID >= 0 && int(edgeID)+1 > maxIndex {
			maxIndex = int(edgeID) + 1
		}
	}
	simulator.store.Grow(maxIndex)
	for edgeID, loadVPH := range loads {
		simulator.store.Set(edgeID, nonNegative(simulator.store.Load(edgeID)+loadVPH))
	}
}

// Step evolves the load over one discrete tick using the seeded RNG. For each
// currently-loaded edge (load > 0) it multiplies the load by a factor drawn
// uniformly from [1-perturbationFraction, 1+perturbationFraction], so loaded
// edges drift up and down deterministically-per-seed while unloaded edges stay
// at 0. The model is intentionally simple and not physically realistic (the §R5
// mesoscopic simulator in Phase 4 is the realistic one); its only contracts are
// that the evolution is live (loaded edges change across ticks, so routes shift)
// and deterministic per seed. Every resulting load is floored at 0 so a load can
// never go negative and break the router's edge-weight non-negativity
// assumption.
//
// Edges are visited in ascending EdgeID order (store index order) so the
// sequence of RNG draws — and therefore the whole evolution — is reproducible
// for a given seed, independent of Go's randomized map iteration order.
func (simulator *Simulator) Step() {
	for index := 0; index < simulator.store.Len(); index++ {
		currentLoad := simulator.store.At(index)
		if currentLoad <= 0 {
			continue
		}
		factor := 1 - perturbationFraction + simulator.rng.Float64()*(2*perturbationFraction)
		simulator.store.Set(domain.EdgeID(index), nonNegative(currentLoad*factor))
	}
}

// StepN advances the simulator by tickCount discrete ticks, equivalent to
// calling Step that many times. A non-positive tickCount is a no-op. It draws
// from the same seeded RNG stream as repeated Step calls, so StepN(n) and n
// successive Step() calls produce an identical load history.
func (simulator *Simulator) StepN(tickCount int) {
	for tick := 0; tick < tickCount; tick++ {
		simulator.Step()
	}
}

// Load returns the current load (vehicles/hour) on the edge, or 0 if the edge
// is out of range or has never been injected, per the
// congestion.CongestionProvider port.
func (simulator *Simulator) Load(edgeID domain.EdgeID) float64 {
	return simulator.store.Load(edgeID)
}

// Snapshot returns a fresh, caller-owned congestion.LoadSnapshot copy of the
// current per-edge loads. Mutating the returned snapshot does not affect the
// simulator, and a later Inject or Step on the simulator does not affect a
// snapshot already handed out — the two own disjoint backing arrays, as the port
// requires.
func (simulator *Simulator) Snapshot() congestion.LoadSnapshot {
	return simulator.store.Snapshot()
}

// nonNegative floors a load at 0, matching how the cost functions floor negative
// load: a negative load would break the router's edge-weight non-negativity
// assumption (Dijkstra).
func nonNegative(loadVPH float64) float64 {
	if loadVPH < 0 {
		return 0
	}
	return loadVPH
}

// Compile-time assertion: *Simulator satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = (*Simulator)(nil)
