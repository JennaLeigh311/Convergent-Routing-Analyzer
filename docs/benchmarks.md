# Benchmarks

The analytics surface that turns a router's assignment into the project's headline
numbers. The reusable core lives in `engine/internal/benchmark`; the Phase-4 CLI
table and the Phase-5 live API both consume it rather than re-deriving metrics.
See `project-spec.md §5`, `§R5`.

> Scope: this documents the **metric methodology** implemented in
> `engine/internal/benchmark` — the #89 static realized-time evaluator and the #90
> discrete-time mesoscopic simulator (below) — AND the six-router demand-sweep
> comparison the Phase-4 `make bench` harness (issue #91) runs on top of it. The
> generated comparison table at the bottom of this file is refreshed by `make bench`
> — see [The demand sweep & comparison table](#the-demand-sweep--comparison-table).

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

### Experienced (over-the-run) metrics vs the static one-shot — the honesty distinction

`SimResult.MeanRealizedS` / `P95RealizedS` are the **experienced (over-the-run)** metrics:
a plain arithmetic **mean** / nearest-rank **p95** over each vehicle's experienced trip
time as congestion evolved across the run — **not** a duration/occupancy/weight-weighted
average. They are **deliberately DISTINCT** from the static-equilibrium
`Result.MeanRealizedS` / `P95RealizedS`, and must be read and labeled as such. The
distinction is **experienced-over-the-run vs static-equilibrium**, not weighted-vs-
unweighted. On a congested toy demand the two **differ** — staggered departures mean
fewer vehicles share an edge at any instant than the static all-at-once load implies, so
the experienced (over-the-run) mean comes out materially lower than the static number.
That difference is the whole point of modeling time, and
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

The OD set is **released in a fixed total order** (`DepartAt`, then input index); each
tick takes **one immutable frozen snapshot** every released request routes against; the
concurrent route fan-out writes each route **by its stable batch index**
(`routeBatchConcurrent` — **no shared accumulator, no lock, and no `combineFlows`-style
reduction**); and the per-edge load is **re-derived serially** on the simulation
goroutine after the fan-out has fully joined, with the per-tick stream emitted in clock
order. A serialized OD set therefore yields a **byte-identical tick-by-tick trace** run
to run — verified on both the single-edge corridor and the route-choice toy network (two
runs plus reversed input, comparing the full per-tick trace, not just the aggregates) —
and the loop is `go test -race` clean. Degenerate inputs are defined: an empty batch runs
zero ticks to an all-zero-but-finite `SimResult`, and an origin == destination request
completes immediately at zero travel time.

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

## The demand sweep & comparison table

`make bench` (`engine/cmd/benchmark`, issue #91) runs the **real six-router
comparison**: all six routers (`naive`, `reactive`, `incremental`, `msa`,
`systemoptimal`, `multipath`) over the **same reproducible OD set** at every level of
a demand sweep, on the toy graph in CI Lane A. It emits the grid as JSON (stdout) and
refreshes the Markdown table below.

**OD set.** `benchmark.GenerateODSet` synthesizes `R = 1000` requests (default) from a
fixed seed, drawing each request's origin→destination from the graph's **reachable**
node-pair pool (a deterministic BFS builds the pool, so an unreachable pair is never
synthesized — the toy network is largely one-way). The set serializes to disk keyed by
coordinates/labels/node ids, **never** by `EdgeID` (the §2 frozen-contract rule). The
summed per-request demand is the fixed `SweepDemandVPH = 5000` vph at every level.

**The sweep axis is `CapacityScale`, not the request count.** Total demand is held
fixed at 5000 vph; the only thing that varies across levels is the BPR
`CapacityScale` (project-spec.md §R3, the frontend's one global capacity knob). Since
the effective capacity is `CapacityVPH × CapacityScale` and `v/c` scales inversely
with it, the four target `v/c` bands are hit by four calibrated scales:

| target `v/c` | `CapacityScale` | realized `max_vc` (naive) |
|---|---|---|
| 0.5 | 2.00 | ≈ 0.50 |
| 0.8 | 1.25 | ≈ 0.80 |
| 1.0 | 1.00 | ≈ 1.01 |
| 1.2 | 0.84 | ≈ 1.20 |

The scales are calibrated empirically against the toy graph's busiest corridor; the
**realized** `max_vc` is reported per row alongside the target, so the small
calibration drift is visible, not hidden. This is the honest answer to "how is the
target `v/c` achieved": one fixed OD set seen against four effective capacities.

**Simulator-mode columns (`sim_mean_s`, `sim_p95_s`).** The **experienced
(over-the-run)** mean / p95 realized travel time from the §R5 discrete-time
**mesoscopic** simulator (`benchmark.Simulate`, issue #90), run once per level: the
level's OD set is released on a fixed sim clock (`StartTime`, `Δt = 30 s`) and each
vehicle advances along its `ReactiveBPRFactory`-routed path at the BPR-derived edge
speed, the per-edge load re-deriving each tick from who is on each edge, so congestion
builds and drains over the run (see
[the mesoscopic simulator section](#discrete-time-mesoscopic-simulator-benchmarksimulate-r5)).
These are **deliberately distinct** from the static `mean_s` / `p95_s`: those assume
every request is simultaneously present at the converged flow, whereas these are the
times vehicles actually experience as the peak fills and drains. Because the OD set
departs all at `t = 0` (the static all-at-once batch), the simulated peak under-counts a
genuinely staggered rush hour, but the lagged-by-one-Δt feedback already makes the
experienced numbers diverge from the static one-shot — they are read as the time-domain
sanity check beside the static columns. The simulator's router is the
congestion-aware reactive factory (not the cell's static router), so the columns
characterize the **level**, attached identically to each router row of that level. The
run is deterministic (fixed `StartTime`/`Δt`/`MaxTicks` over the reproducible OD set),
so the columns are byte-identical run to run.

**Headline numbers — honest, with their demand level.** Improvement is `naive` vs. the
**best of `incremental`/`systemoptimal`**, reported with the demand level at which it
is largest (never a single cherry-picked figure: PoA peaks at moderate load and → 1 at
both extremes). On `toy_network.geojson` the network has **no Braess/Pigou structure**,
so `naive`, `reactive`, and the convergent routers (`incremental`/`msa`/`systemoptimal`)
all land on the same split: **`PoA ≈ 1.00` at every level and the improvement is ≈ 0%**.
That is the truthful result on this fixture — the strict `PoA > 1` demonstration is the
dedicated Pigou fixture above. The one router that diverges is `multipath`, whose
probabilistic K-shortest split deliberately fans demand onto the slower direct edge,
realizing `PoA ≈ 1.16–1.18` — a faithful picture of what a randomized split costs when
the optimum is a single corridor.

**Determinism.** The JSON artifact carries no wall-clock field; all randomness flows
through fixed seeds (OD draw, multipath split) and the simulator-mode columns run under
a fixed sim clock (`StartTime`/`Δt`/`MaxTicks`) over the same reproducible OD set, and
every aggregate iterates sorted copies, so two `make bench` runs produce byte-identical
JSON. The wall-clock `time=`/`elapsed=` fields go to stderr (the log), out of the
diffable stdout JSON.

The table below is **generated** — `make bench` rewrites everything between the
markers. Do not edit it by hand.

<!-- BENCH-TABLE:BEGIN (generated by cmd/benchmark — do not edit by hand) -->

| router | demand | cap_scale | target_vc | max_vc | mean_s | p95_s | total_s | poa | sim_mean_s | sim_p95_s | gini_vc | iters | converged | gap |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| naive | vc0.5 | 2.00 | 0.50 | 0.5030 | 33.98 | 78.01 | 169894.56 | 1.0000 | 33.85 | 77.76 | 0.4184 | 1 | true | 0 |
| reactive | vc0.5 | 2.00 | 0.50 | 0.5030 | 33.98 | 78.01 | 169894.56 | 1.0000 | 33.85 | 77.76 | 0.4184 | 1 | true | 0 |
| incremental | vc0.5 | 2.00 | 0.50 | 0.5030 | 33.98 | 78.01 | 169894.56 | 1.0000 | 33.85 | 77.76 | 0.4184 | 4 | true | 1.7130525183456236e-16 |
| msa | vc0.5 | 2.00 | 0.50 | 0.5030 | 33.98 | 78.01 | 169894.56 | 1.0000 | 33.85 | 77.76 | 0.4184 | 2 | true | 1.7130525183456236e-16 |
| systemoptimal | vc0.5 | 2.00 | 0.50 | 0.5030 | 33.98 | 78.01 | 169894.56 | 1.0000 | 33.85 | 77.76 | 0.4184 | 2 | true | 3.3737938372105614e-16 |
| multipath | vc0.5 | 2.00 | 0.50 | 0.5030 | 40.18 | 135.64 | 200897.80 | 1.1825 | 33.85 | 77.76 | 0.3320 | 1 | true | 0 |
| naive | vc0.8 | 1.25 | 0.80 | 0.8048 | 34.71 | 79.42 | 173551.94 | 1.0000 | 33.85 | 77.76 | 0.4184 | 1 | true | 0 |
| reactive | vc0.8 | 1.25 | 0.80 | 0.8048 | 34.71 | 79.42 | 173551.94 | 1.0000 | 33.85 | 77.76 | 0.4184 | 1 | true | 0 |
| incremental | vc0.8 | 1.25 | 0.80 | 0.8048 | 34.71 | 79.42 | 173551.94 | 1.0000 | 33.85 | 77.76 | 0.4184 | 4 | true | 3.353904335759908e-16 |
| msa | vc0.8 | 1.25 | 0.80 | 0.8048 | 34.71 | 79.42 | 173551.94 | 1.0000 | 33.85 | 77.76 | 0.4184 | 2 | true | 3.353904335759908e-16 |
| systemoptimal | vc0.8 | 1.25 | 0.80 | 0.8048 | 34.71 | 79.42 | 173551.94 | 1.0000 | 33.85 | 77.76 | 0.4184 | 2 | true | 1.5252324258202472e-16 |
| multipath | vc0.8 | 1.25 | 0.80 | 0.8048 | 40.92 | 137.20 | 204595.42 | 1.1789 | 33.85 | 77.76 | 0.3320 | 1 | true | 0 |
| naive | vc1.0 | 1.00 | 1.00 | 1.0060 | 35.95 | 81.82 | 179772.97 | 1.0000 | 33.85 | 77.77 | 0.4184 | 1 | true | 0 |
| reactive | vc1.0 | 1.00 | 1.00 | 1.0060 | 35.95 | 81.82 | 179772.97 | 1.0000 | 33.85 | 77.77 | 0.4184 | 1 | true | 0 |
| incremental | vc1.0 | 1.00 | 1.00 | 1.0060 | 35.95 | 81.82 | 179772.97 | 1.0000 | 33.85 | 77.77 | 0.4184 | 4 | true | 0 |
| msa | vc1.0 | 1.00 | 1.00 | 1.0060 | 35.95 | 81.82 | 179772.97 | 1.0000 | 33.85 | 77.77 | 0.4184 | 2 | true | 0 |
| systemoptimal | vc1.0 | 1.00 | 1.00 | 1.0060 | 35.95 | 81.82 | 179772.97 | 1.0000 | 33.85 | 77.77 | 0.4184 | 2 | true | 1.3114510281200742e-16 |
| multipath | vc1.0 | 1.00 | 1.00 | 1.0060 | 42.18 | 139.86 | 210884.87 | 1.1731 | 33.85 | 77.77 | 0.3320 | 1 | true | 0 |
| naive | vc1.2 | 0.84 | 1.20 | 1.1976 | 38.08 | 85.92 | 190400.03 | 1.0000 | 33.85 | 77.77 | 0.4184 | 1 | true | 0 |
| reactive | vc1.2 | 0.84 | 1.20 | 1.1976 | 38.08 | 85.92 | 190400.03 | 1.0000 | 33.85 | 77.77 | 0.4184 | 1 | true | 0 |
| incremental | vc1.2 | 0.84 | 1.20 | 1.1976 | 38.08 | 85.92 | 190400.03 | 1.0000 | 33.85 | 77.77 | 0.4184 | 4 | true | 3.057124495466421e-16 |
| msa | vc1.2 | 0.84 | 1.20 | 1.1976 | 38.08 | 85.92 | 190400.03 | 1.0000 | 33.85 | 77.77 | 0.4184 | 2 | true | 3.057124495466421e-16 |
| systemoptimal | vc1.2 | 0.84 | 1.20 | 1.1976 | 38.08 | 85.92 | 190400.03 | 1.0000 | 33.85 | 77.77 | 0.4184 | 2 | true | 2.1162099537297466e-16 |
| multipath | vc1.2 | 0.84 | 1.20 | 1.1976 | 44.33 | 144.39 | 221628.84 | 1.1640 | 33.85 | 77.77 | 0.3320 | 1 | true | 0 |

<!-- BENCH-TABLE:END -->
