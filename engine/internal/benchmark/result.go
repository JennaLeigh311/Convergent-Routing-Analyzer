package benchmark

import (
	"fmt"
	"strings"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// Result is the metric bundle for one (router, demand level) cell of the benchmark
// table: the realized-travel-time aggregates plus the convergence metadata passed
// through from the AssignResult. It is the single value type both the Phase-4 CLI
// table and the Phase-5 live dashboard consume, so it is BOTH:
//
//   - JSON-serializable, with explicit lowercase `json:` tags, for the dashboard
//     stream and any saved results artifact (the tags are the wire contract — do
//     not rename a field without bumping the consumers, the same discipline the
//     §-contracts hold).
//   - Markdown-renderable, via MarkdownHeader/MarkdownSeparator/MarkdownRow, so docs
//     and the CLI can assemble a results table without re-knowing the field set.
//
// PriceOfAnarchy is NOT a field of Result: it is a relationship BETWEEN two results
// (selfish vs reference) on the same OD set, not a property of a single (router,
// demand) cell — computing it needs the other router's total, which a lone Result
// does not have. It is the package-level PriceOfAnarchy function instead, so a
// Result stays a self-contained, single-assignment record. The CLI layers PoA on
// top by dividing two cells' TotalNetworkTimeS.
//
// All fields are finite and well-defined on the empty batch and on zero-flow edges
// (means/percentiles default to 0, Gini to 0, MaxVC to 0), so a Result is always
// safe to serialize and render — there is no NaN/Inf to special-case downstream.
type Result struct {
	// Router is the strategy name (routing.Router.Name(): "naive", "msa",
	// "systemoptimal", …) this cell measured. It is the row's identity in the table.
	Router string `json:"router"`

	// DemandLevel is the human label for the demand scenario this cell ran under
	// (e.g. "light", "congested") — the column identity. It is a free-form caller
	// label, not derived, because the demand sweep that defines the levels is the
	// CLI's concern, not the evaluator's.
	DemandLevel string `json:"demand_level"`

	// MeanRealizedS is the mean realized per-request travel time, seconds — BPR.Cost
	// applied to the realized edge loads, averaged over the requests in input order.
	MeanRealizedS float64 `json:"mean_realized_s"`

	// P95RealizedS is the 95th-percentile realized per-request travel time, seconds,
	// by nearest-rank on a sorted copy (the tail a mean hides — the unlucky driver).
	P95RealizedS float64 `json:"p95_realized_s"`

	// TotalNetworkTimeS is the realized total system travel time, seconds:
	// routing.TotalNetworkTime(g, bpr, FinalFlows) = Σ_e flow_e × BPR.Cost_e. It is
	// the basis of the Price of Anarchy and the SO ≤ UE invariant.
	TotalNetworkTimeS float64 `json:"total_network_time_s"`

	// GiniVC is the Gini coefficient of v/c across all edges in [0,1]: edge-
	// utilization BALANCE — 0 when every edge is equally loaded, toward 1 as load
	// concentrates. A lower Gini means the assignment spread demand more evenly.
	GiniVC float64 `json:"gini_vc"`

	// MaxVC is the maximum volume/capacity ratio over all edges — the single most
	// congested edge. v/c > 1 means an edge is loaded past its effective capacity.
	MaxVC float64 `json:"max_vc"`

	// Iters is the number of assignment iterations the router performed (passed
	// through from AssignResult; single-pass routers report 1).
	Iters int `json:"iters"`

	// Converged reports whether the router reached its convergence criterion within
	// its iteration budget (passed through from AssignResult).
	Converged bool `json:"converged"`

	// Gap is the achieved relative convergence gap at the reported iteration (passed
	// through from AssignResult; single-pass routers report 0).
	Gap float64 `json:"gap"`
}

// Evaluate computes the full Result for one router's assignment over one demand
// level. It is the package's primary entry point: hand it the graph, the BPR cost
// function, the router/demand labels, and the AssignResult the router produced, and
// it returns the metric bundle.
//
// It is deterministic — every aggregate it calls iterates a sorted copy of its
// input in fixed order (RealizedTimes in input order; the percentile, Gini, and
// MaxVC over sorted/edge-id order) and never mutates result — so two Evaluate calls
// over the same inputs return an == Result and identical JSON. It does NOT run the
// router or pick the demand: the caller owns router construction and the OD set, so
// the evaluator stays router-agnostic and the same Result shape serves the CLI and
// the live API.
//
// The convergence metadata (Iters/Converged/Gap) is passed through from result
// verbatim — the evaluator measures realized outcomes, it does not re-judge
// convergence.
func Evaluate(g graph.Graph, bpr cost.BPR, router, demandLevel string, result routing.AssignResult) Result {
	realized := RealizedTimes(g, bpr, result)
	vc := edgeVCRatios(g, bpr, result.FinalFlows)

	return Result{
		Router:            router,
		DemandLevel:       demandLevel,
		MeanRealizedS:     mean(realized),
		P95RealizedS:      percentileNearestRank(realized, 0.95),
		TotalNetworkTimeS: routing.TotalNetworkTime(g, bpr, result.FinalFlows),
		GiniVC:            gini(vc),
		MaxVC:             maxOf(vc),
		Iters:             result.Iters,
		Converged:         result.Converged,
		Gap:               result.Gap,
	}
}

// markdownColumns is the single source of truth for the table's column order, used
// by both the header and the row so the two can never drift out of alignment.
var markdownColumns = []string{
	"router", "demand", "mean_s", "p95_s", "total_s", "gini_vc", "max_vc", "iters", "converged", "gap",
}

// MarkdownHeader returns the table's header row (no trailing newline), e.g.
//
//	| router | demand | mean_s | p95_s | total_s | gini_vc | max_vc | iters | converged | gap |
//
// Pair it with MarkdownSeparator and one MarkdownRow per Result to assemble a
// GitHub-flavored Markdown table in docs or the CLI. It is a package function (not a
// method) because the header is independent of any single Result.
func MarkdownHeader() string {
	return "| " + strings.Join(markdownColumns, " | ") + " |"
}

// MarkdownSeparator returns the header/body separator row matching MarkdownHeader's
// column count, e.g. "| --- | --- | … |". Emitted once, between the header and the
// first row.
func MarkdownSeparator() string {
	dashes := make([]string, len(markdownColumns))
	for i := range dashes {
		dashes[i] = "---"
	}
	return "| " + strings.Join(dashes, " | ") + " |"
}

// MarkdownRow renders this Result as one Markdown table row (no trailing newline),
// in the MarkdownHeader column order. Floats are formatted to two decimals (the
// table is for human reading; the JSON form carries full precision for machines),
// except Gap which uses a compact %g because a convergence gap spans many orders of
// magnitude (1.0 down to ~1e-4). The deterministic, fixed-precision formatting keeps
// a rendered table byte-stable for a given Result.
func (r Result) MarkdownRow() string {
	cells := []string{
		r.Router,
		r.DemandLevel,
		fmt.Sprintf("%.2f", r.MeanRealizedS),
		fmt.Sprintf("%.2f", r.P95RealizedS),
		fmt.Sprintf("%.2f", r.TotalNetworkTimeS),
		fmt.Sprintf("%.4f", r.GiniVC),
		fmt.Sprintf("%.4f", r.MaxVC),
		fmt.Sprintf("%d", r.Iters),
		fmt.Sprintf("%t", r.Converged),
		fmt.Sprintf("%g", r.Gap),
	}
	return "| " + strings.Join(cells, " | ") + " |"
}
