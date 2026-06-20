# Benchmarks

The analytics surface that turns a router's assignment into the project's headline
numbers. The reusable core lives in `engine/internal/benchmark`; the Phase-4 CLI
table and the Phase-5 live API both consume it rather than re-deriving metrics.
See `project-spec.md §5`, `§R5`.

> Scope: this documents the **metric methodology** implemented in
> `engine/internal/benchmark` (issue #89). The mesoscopic simulator, the demand
> sweep, the `make bench` CLI table, and the populated results tables land in later
> Phase-4 issues and are not covered here yet.

## Routing cost vs realized travel time — the honesty distinction

The single most important distinction (`§5`) is between the cost a router
**optimized against** and the time a driver **actually experiences**:

- **Routing cost** (`routing.Route.CostS`) is the weight the router chose its path
  under — free-flow time for `naive`, the BPR marginal cost for `systemoptimal`, and
  so on. It is what the router *believed* the path would cost. It is **not**
  comparable across strategies: two routers optimizing different weights are not
  measuring the same thing.
- **Realized travel time** is the ground truth, computed **after** assignment by
  applying the BPR cost function to the **actual per-edge flow** the assignment
  produced (`routing.AssignResult.FinalFlows`). Two routers can pick the same path
  yet realize different times because they load the rest of the network differently.

Only the realized time is comparable across strategies, so every metric below is
built on it — never on `CostS`. The realized **total** is
`routing.TotalNetworkTime(g, bpr, flows) = Σ_e flow_e × BPR.Cost(e, flow_e)`, the
same evaluator the SO ≤ UE invariant test is proven on; the benchmark reuses that one
implementation rather than re-summing.

## The metrics (`benchmark.Result`)

`benchmark.Evaluate(g, bpr, router, demandLevel, assignResult)` returns a `Result`
per `(router, demand level)`. It is JSON-serializable (lowercase `json:` tags, the
dashboard wire contract) and Markdown-renderable (`MarkdownHeader` /
`MarkdownSeparator` / `MarkdownRow`). Its fields:

| field | meaning |
|---|---|
| `mean_realized_s` | Mean realized per-request travel time (seconds). The realized time of one request is `Σ BPR.Cost(edge, flow_edge)` over its path edges. |
| `p95_realized_s` | 95th-percentile realized per-request time, by **nearest-rank** on a sorted copy. Nearest-rank (not interpolation) is deterministic and always an observed value, so it reproduces exactly across runs and platforms. This is the tail the mean hides — the unlucky driver. |
| `total_network_time_s` | Realized total system travel time `Σ_e flow_e × BPR.Cost_e` (`routing.TotalNetworkTime`). The basis of the Price of Anarchy and the SO ≤ UE invariant. |
| `gini_vc` | **Edge-utilization balance**: the Gini coefficient of `v/c` across all edges, in `[0, 1]`. `0` = every edge equally loaded (perfectly balanced); toward `1` = load concentrated on few edges. |
| `max_vc` | Maximum `v/c` over all edges — the single most congested edge. `v/c > 1` means an edge is loaded past its effective capacity. |
| `iters`, `converged`, `gap` | Convergence metadata passed through verbatim from the `AssignResult` (`§R5`); the evaluator measures realized outcomes, it does not re-judge convergence. |

**`v/c` definition.** `v` is the realized edge flow; `c` is the **effective**
capacity `CapacityVPH × CapacityScale` — defined exactly as `BPR.Cost` defines it, so
the utilization metric and the cost curve agree on what "capacity" means. An edge with
non-positive effective capacity (the case `BPR.Cost` falls back to free-flow on) is
treated as `v/c = 0` rather than `+Inf`, keeping every aggregate finite.

**Determinism.** Every aggregate iterates a sorted copy of its input in a fixed order
(realized times in input order; the percentile, Gini, and `v/c` in sorted / edge-id
order) and never mutates the caller's data, so a fixed seed plus a serialized OD set
reproduces a byte-identical `Result` and identical JSON run to run.

**Degenerate inputs.** All metrics are finite and defined on the empty batch (means /
percentiles / Gini / `max_vc` default to `0`) and on zero-flow edges, so a `Result` is
always safe to serialize and render with no `NaN`/`Inf` to special-case downstream.

## Price of Anarchy — the headline number

The Price of Anarchy is the realized-time ratio of the **selfish** baseline to a
**coordinated reference**, both on the **same OD set**:

```
PoA = TotalNetworkTime(selfish) / TotalNetworkTime(reference)
```

- The **selfish** baseline is `naive` — free-flow shortest path, every driver routing
  as if alone, ignoring the congestion they collectively cause.
- The **reference** is `systemoptimal` — the coordinated optimum that internalizes the
  congestion externality.

`PoA ≥ 1` whenever the reference is a true optimum; `PoA > 1` is exactly the gap the
coordinated routing closes. `PoA = 2.0` means selfish routing realizes twice the total
travel time the optimum does.

`benchmark.PriceOfAnarchy(selfishTotal, referenceTotal)` is deliberately
**router-agnostic**: it takes the two precomputed totals, not routers or results, so
the metric never hardcodes which strategy is the baseline — the caller decides, and the
same function serves both the CLI table and the live API. Degenerate totals (a
non-positive reference or selfish total) yield `PoA = 1`, never a `NaN`/`Inf`.

### Why a dedicated Pigou fixture

The hand-built `toy_network.geojson` has no Braess/Pigou structure, so its
`PoA ≈ 1` — selfish and optimal land on essentially the same split. Demonstrating a
strict `PoA > 1` requires a network where selfish routing genuinely overloads a link.
`engine/testdata/toy_network_pigou.geojson` (issue #89) is that fixture: the classic
two-link Pigou network — a cheap, low-capacity link in parallel with a near-constant,
high-capacity one. At a congested demand level the `naive` router piles all demand onto
the cheap link (`v/c = 2`), while `systemoptimal` splits it, giving a measured
**`PoA(naive) ≈ 2.17`** with `systemoptimal` realized total ≤ `naive`'s. See
`engine/testdata/README.md` for the fixture's full derivation.
