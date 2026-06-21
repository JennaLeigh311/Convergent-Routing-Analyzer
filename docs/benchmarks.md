# Benchmarks

The analytics surface that turns a router's assignment into the project's headline
numbers. The reusable core lives in `engine/internal/benchmark`; the Phase-4 CLI
table and the Phase-5 live API both consume it rather than re-deriving metrics.
See `project-spec.md §5`, `§R5`.

> Scope: this documents the **metric methodology** implemented in
> `engine/internal/benchmark` — the #89 static realized-time evaluator and the #90
> discrete-time mesoscopic simulator (below). The demand sweep, the `make bench` CLI
> table, and the populated results tables land in later Phase-4 issues and are not
> covered here yet.

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
per `(router, demand level)`. It is JSON-serializable (lowercase `json:` tags — an
internal serialization shape today; to be registered in `docs/contracts.md` as a
frozen envelope when the Phase-5 API/dashboard serializes it) and Markdown-renderable
(`MarkdownHeader` /
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

## Discrete-time mesoscopic simulator (`benchmark.Simulate`, `§R5`)

The static `Evaluate` metrics above are honest **only as a static equilibrium**: they
assume every request is simultaneously present at the converged flow. They do **not**
model "congestion builds over a peak and then dissipates". `§R5` mandates a
discrete-time **mesoscopic** simulator for that, implemented by
`benchmark.Simulate(ctx, g, reqs, cfg, routerFactory, observer)`:

- Requests carry a **`DepartAt`** (seconds from `cfg.StartTime`) and depart on a sim
  clock that advances in **Δt** steps (`cfg.TickSeconds`, default ≈30s).
- Each tick, newly-released requests route against the **current per-edge load** — one
  **immutable congestion snapshot value per tick**, so every request released that tick
  sees an identical, stable view (the same per-round snapshot discipline the reactive /
  iterative routers use). The routing is the **router-agnostic** `RouterFactory` seam;
  `ReactiveBPRFactory` is the congestion-aware default.
- In-flight vehicles advance along their routed path at the **BPR-derived edge speed**
  `edge.LengthM / BPR.Cost(edge, load)` (m/s), carrying the remainder across edges; the
  per-edge load is **re-derived each tick from who is on each edge**, so congestion
  builds as the peak fills and **dissipates** as it drains.
- Each vehicle's **realized experienced** travel time is recorded the tick it completes.

### Time-weighted metrics vs the static one-shot — the honesty distinction

`SimResult.MeanRealizedS` / `P95RealizedS` are **time-weighted**: the mean / p95 of the
times vehicles **actually experienced** as congestion evolved over the run. They are
**deliberately DISTINCT** from the static-equilibrium `Result.MeanRealizedS` /
`P95RealizedS`, and must be read and labeled as such. On a congested toy demand the two
**differ** — staggered departures mean fewer vehicles share an edge at any instant than
the static all-at-once load implies, so the time-weighted mean comes out materially
lower than the static number. That difference is the whole point of modeling time, and
it is what makes the "1,000 requests over a rush hour" narrative defensible. Both
numbers are computed with the **same #89 evaluator helpers** (`mean`,
`percentileNearestRank`) — the simulator reuses them rather than re-deriving — so both
share the empty-batch / finiteness contract; only the **inputs** differ (experienced
times over the run vs realized times at the converged static flow).

### Per-tick state seam (the Phase-5 `#93` plug point)

`Simulate` streams an immutable **`TickState`** (sim clock instant, per-edge load and
`v/c`, in-flight / completed counts) to a **`TickObserver`** callback once per tick, in
strict tick order — the explicit seam the Phase-5 WebSocket backend (`#93`) plugs into to
animate the evolving congestion. The `cfg.StartTime` **time-of-day/date** origin sets the
demand/clock and stamps every `TickState.SimTime`, so the frontend's time-of-day slider
maps straight onto it (shifting `StartTime` shifts every tick's clock, dynamics
unchanged).

### Determinism (`§R5`)

The OD set is released in a fixed sorted order (`DepartAt`, then input index), the
per-tick congestion is one immutable snapshot value, the **sharded** per-goroutine route
accumulation is reduced once per tick in fixed worker-then-edge order (mirroring the
assignment core's `combineFlows`, with **no shared mutable map under a lock**), and the
per-tick stream emits in clock order. A fixed seed plus a serialized OD set therefore
yields a **byte-identical tick-by-tick trace** run to run, and the loop is
`go test -race` clean. Degenerate inputs are defined: an empty batch runs zero ticks to
an all-zero-but-finite `SimResult`, and an origin == destination request completes
immediately at zero travel time.

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
