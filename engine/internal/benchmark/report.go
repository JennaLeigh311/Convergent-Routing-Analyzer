package benchmark

import (
	"encoding/json"
	"io"
)

// Report is the full serialized benchmark artifact: the sweep grid (every
// (router, level) cell), the per-level Price of Anarchy, and the headline
// improvement with its demand level. It is the single value the CLI emits as JSON
// and the dashboard (Phase 5) can later consume.
//
// DETERMINISM: every field is derived purely from the routers' deterministic
// assignments over the reproducible OD set — no wall-clock, no RNG outside the
// fixed sweep/sim/multipath seeds — so two BuildReport calls over the same graph
// and seed marshal to byte-identical JSON. The CLI deliberately keeps any
// wall-clock timing OUT of this struct (it logs runtime separately) so the JSON
// artifact is diffable run to run, satisfying the §R5 determinism criterion.
type Report struct {
	// Seed and ODCount record the inputs so the artifact is self-describing and
	// reproducible.
	Seed    int64 `json:"seed"`
	ODCount int   `json:"od_count"`

	// TotalDemandVPH is the fixed total demand every level routed at (SweepDemandVPH).
	TotalDemandVPH float64 `json:"total_demand_vph"`

	// Cells is the comparison grid, one per (router, level), in (level,
	// RouterOrder) order.
	Cells []SweepCell `json:"cells"`

	// PoAByLevel is the naive-vs-systemoptimal Price of Anarchy at each of the four
	// sweep levels — reported across ALL levels, never a single cherry-picked
	// figure (project-spec.md §5: PoA peaks at moderate load and → 1 at the extremes).
	PoAByLevel []LevelPoA `json:"poa_by_level"`

	// Headline is the single improvement % WITH its demand level attached: naive vs.
	// the best of incremental / systemoptimal, at the level where the reduction is
	// largest.
	Headline Improvement `json:"headline_improvement"`
}

// BuildReport assembles a Report from a sweep grid: it attaches the per-level PoA
// and the headline improvement so the JSON artifact and the CLI summary read from
// one place. seed/odCount are echoed for self-description.
func BuildReport(seed int64, odCount int, cells []SweepCell) Report {
	return Report{
		Seed:           seed,
		ODCount:        odCount,
		TotalDemandVPH: SweepDemandVPH,
		Cells:          cells,
		PoAByLevel:     PoAByLevel(cells),
		Headline:       HeadlineImprovement(cells),
	}
}

// WriteJSON serializes the report as indented JSON (deterministic field order from
// the struct tags; cells/levels in their deterministic generation order), so the
// saved artifact is diffable and reproducible. It writes no EdgeID and no
// wall-clock field, honoring both the §2 frozen-contract rule and the §R5
// determinism criterion.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
