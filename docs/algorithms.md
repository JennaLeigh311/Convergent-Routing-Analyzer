# Algorithms

The traffic-assignment catalog and the volume-delay cost model it routes on. This document covers the
shipped Phase-1/Phase-2 pieces (the BPR + linear cost functions, the marginal-cost form, and the reactive
best-response router) and the planned Phase-3+ algorithms (incremental, MSA, system-optimal, multipath),
clearly separated. Expanded from `project-spec.md §1`, `§4`, `§5`, `§R3`, and `§R5`.

The framing (`project-spec.md §1`): the "convergent routing" problem is the transportation-science
**traffic assignment problem**, which gives a well-studied catalog of algorithms to implement and compare.
The headline metric is the **Price of Anarchy (PoA)** — the gap between *selfish* routing (naive shortest
path, where every request piles onto the same fastest edge) and the system optimum. PoA is exactly the gap
this project claims to close.

## 1. The cost model

An algorithm's choices are driven by an edge **cost function**: a map from an edge and its current load to a
traversal time in seconds. The port is `CostFunction` (`engine/internal/cost/cost.go`):

```go
type CostFunction interface {
	Cost(edge graph.Edge, loadVPH float64) float64
}
```

`loadVPH` is the edge's flow in **vehicles/hour**; both the load and the edge's `CapacityVPH` must share that
unit. That unit contract is canonical in `docs/contracts.md §2` (unit contract) and `§3` (the `vehicle_count`
row), and summarized for the pipeline side in `docs/data-pipeline.md`. With `β = 4`, a unit mismatch is raised
to the fourth power and silently corrupts the headline number — hence a *written* contract (`project-spec.md
§R3`).

Two implementations ship in Phase 2: `BPR` (the reference) and `Linear` (a cheap baseline).

### 1.1 BPR (Bureau of Public Roads)

The reference volume-delay function (`engine/internal/cost/bpr.go`):

```
t(v) = t_free · (1 + α·(v/c)^β)
```

where `v` is the load (vehicles/hour), `c` is the edge's **effective capacity**, and `t_free` is the edge's
free-flow time (`graph.Edge.FreeFlowS`). The conventional Bureau of Public Roads coefficients are `α = 0.15`
and `β = 4` (`project-spec.md §1`). `α` scales the congestion penalty; `β` sets how sharply cost blows up past
capacity.

- Construct the conventional coefficients with `cost.DefaultBPR()` (`α = 0.15`, `β = 4`, `CapacityScale = 1.0`).
- `cost.NewBPR(alpha, beta, capacityScale)` takes explicit coefficients; it **panics on a non-positive
  `capacityScale`**, since a zero/negative scale would silently switch off congestion modeling for every edge.

**Edge-case behavior (matches the code exactly):**

- A non-positive *effective* capacity (a zero/negative `CapacityVPH`, or a non-positive `CapacityScale`) falls
  back to the **free-flow time** `t_free`, to avoid a divide-by-zero.
- A negative `loadVPH` is out of contract; it is **floored to zero**, so the result never drops below `t_free`.
  This keeps every edge weight `≥ FreeFlowS ≥ 0`, which Dijkstra's correctness requires (no negative weights).
- `β` is conventionally the integer `4`. On the router's hot path it is evaluated by **exponentiation by
  squaring** (a handful of multiplications) rather than the general log/exp-based `math.Pow`; a fractional
  exponent falls back to `math.Pow`.

### 1.2 `CapacityScale` — the one capacity knob

Both `BPR` and `Linear` carry a single global multiplier `CapacityScale` (`project-spec.md §R3`):

```
effective capacity = edge.CapacityVPH × CapacityScale
```

It is the frontend's one tunable capacity "knob" for sensitivity sweeps. A value below `1.0` shrinks effective
capacity and so lifts the cost curve at a fixed load. Both `NewBPR` and `NewLinear` **panic** on a non-positive
scale (a construction-time misconfiguration that would collapse every edge to the free-flow fallback). The
`edge_attributes` export is generated at `capacity_scale = 1.0` and the engine applies any other scale at
runtime (`docs/contracts.md §2`).

### 1.3 Linear — the cheap baseline

A linear-in-`(v/c)` alternative (`engine/internal/cost/linear.go`):

```
t(v) = t_free · (1 + slope·(v/c))
```

Default `slope = 0.15` via `cost.DefaultLinear()` (`cost.NewLinear(slope, capacityScale)` for explicit values).
At a load equal to effective capacity (`v = c`) it adds the same proportional penalty conventional BPR adds at
capacity (`α = 0.15`), so **the two functions line up at `v = c`** and diverge only in curvature: the linear
congestion term grows only linearly, so Linear neither blows up past capacity nor matches the empirical
sharpness of real congestion. It is offered as a well-behaved baseline against which BPR's super-linear
behavior can be compared. Free-flow fallback and negative-load flooring are identical to BPR.

### 1.4 Marginal cost (for the future system-optimal router)

`BPR` exposes a second method, `MarginalCost`, that the planned `systemoptimal` router will route on
(`project-spec.md §R5`):

```
t_free · (1 + α(β+1)·(v/c)^β)
```

This is `t(v) + v·t'(v)`: the delay a marginal vehicle imposes on **everyone already on the edge**, not just on
itself — the externality a selfish driver ignores. It is deliberately **NOT** part of the `CostFunction` port;
only the concrete `BPR` type exposes `MarginalCost`. It is **`≥ Cost` for `v > 0` and equal at `v = 0`**. Same
free-flow fallback and negative-load flooring as `Cost`.

This is the lever for the System-Optimal invariant (`project-spec.md §R5`): on the toy network, **SO total time
`≤` UE total time** — routing on marginal cost internalizes the externality and so minimizes *total* network
time. (No selfish navigator reaches SO on its own; it requires a coordinator, `project-spec.md §1`.)

## 2. The algorithm catalog

The six traffic-assignment strategies (`project-spec.md §4`). All implement the `Router` port
(`engine/internal/routing/routing.go`); the shared Dijkstra core lives in `dijkstra.go`.

| # | Algorithm | Idea | Target | Status |
|---|-----------|------|--------|--------|
| 1 | `naive` | Dijkstra on static free-flow weights; every request independent | baseline (selfish routing) | **Shipped (Phase 1)** |
| 2 | `reactive` | Dijkstra on a **frozen congestion snapshot** (BPR applied to current load) | best-response to stale state — **herds/oscillates, does NOT converge to UE** | **Shipped (Phase 2)** |
| 3 | `incremental` | Assign requests in batches; re-weight edges with BPR after each batch | between UE and SO | Planned (Phase 3) |
| 4 | `msa` | Method of Successive Averages → converges to **User Equilibrium** | UE | Planned (Phase 3) |
| 5 | `systemoptimal` | Re-weight by **marginal** cost (driver pays for delay imposed on others) → minimizes total time | **SO** (theoretical floor) | Planned (Phase 3) |
| 6 | `multipath` | Yen's K-shortest paths + proportional/probabilistic split of N requests across K paths | demand spreading | Planned (Phase 3) |

**Implementation status (honest).** Phase 1 shipped `naive`; Phase 2 shipped `reactive` along with the
BPR/linear cost functions and the congestion providers. The remaining four strategies (`incremental`, `msa`,
`systemoptimal`, `multipath`), the **A\*** single-pair query path and the **Yen K-shortest** core, and the
mesoscopic simulator are **not built yet** — they are planned for Phase 3 (algorithms) and Phase 4 (simulator).
Treat everything in rows 3–6 as design, not present code.

The story the benchmark tells (`project-spec.md §4`): `naive` exhibits a Price of Anarchy `> 1`; `reactive`
helps but herds/oscillates (it is NOT user equilibrium — only `msa` converges to UE); `incremental`/`msa`
stabilize; `systemoptimal` is the floor on total travel time.

## 3. The reactive router (Phase 2)

`reactive` (`engine/internal/routing/reactive.go`) is the **best-response-to-stale-state** strategy: it runs
Dijkstra over edge weights computed by applying a BPR `CostFunction` to a **single FROZEN congestion snapshot**.

```go
router := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)
```

The router holds only the immutable graph, a stateless cost function, and a `congestion.CongestionProvider`
(any adapter — in-memory, static, or simulator — plugs in here). It takes **one** `Snapshot()` per `Route` /
per `Assign` and weights every edge against it, so a result is deterministic for a fixed snapshot + OD set, and
its methods are safe for concurrent use (`project-spec.md §R5`). Because `BPR.Cost ≥ FreeFlowS ≥ 0`, the
Dijkstra weights stay non-negative.

### 3.1 Why it herds — and does NOT converge

This is the heart of the document. Reactive is *honest best-response to stale state*, and best-response to
stale state **herds and oscillates rather than converging to a User Equilibrium**:

- Every request in a round reacts to the **same frozen load**, and **none of them sees the congestion the round
  itself creates**.
- So they all pile onto the **currently-cheapest** path, overloading it.
- The next snapshot then sends the herd to the *next*-cheapest path — a cycle that does **not** settle.

`reactive` is in the catalog precisely to exhibit that non-convergence, not to be a good assignment; do not read
its output as an equilibrium. By contrast, only `msa` converges to **UE** (the Nash state where no single driver
can improve their own time by switching paths, `project-spec.md §1`), and `systemoptimal` (marginal cost, §1.4)
is the **floor** on total network time.

The static one-shot reactive assignment is honest only as a **static equilibrium baseline**; it does NOT model
"congestion builds and dissipates over a peak." That narrative requires the mesoscopic simulator (Phase 4,
`project-spec.md §R5`).

### 3.2 Routing cost vs realized travel time

A subtle but load-bearing distinction (`project-spec.md §5`): the **routing cost** an algorithm optimizes
against is *not* the **realized travel time** the benchmark computes. Realized time is ground truth, derived by
applying BPR to the **actual flow each edge ends up carrying** once all requests are placed.

This is why the reactive CLI prints a *congested BPR cost*, not a realized travel time: `Route.CostS` is the
summed congested BPR cost of the chosen edges under the snapshot — the cost the path was optimized against. The
benchmark harness derives realized time separately from final edge flows (see `docs/benchmarks.md`, planned for
Phase 4).

### 3.3 The Phase-2 demo (the runnable proof)

From `engine/`:

```
go run ./cmd/route --algo reactive --jam 905512:0:F
```

`--jam` injects a heavy load onto the named `segment_id`, lifting its congested BPR cost. On the toy graph this
**diverts the route off the jammed motorway corridor (`905512:0:F`) onto the residential edge** — and prints a
*different* path than `go run ./cmd/route --algo naive`, which still takes the free-flow-cheapest motorway
corridor. This is the runnable proof that the same route changes under injected congestion — the Phase-2
deliverable (`project-spec.md §6`).

## 4. Price of Anarchy — the headline metric

PoA is the project's headline number (`project-spec.md §1`, `§4`). `naive` is selfish routing; the improvement
the project quotes is `naive` vs. the best of `incremental`/`systemoptimal`.

**It must be quoted as a demand sweep, not a single figure.** The Price of Anarchy **peaks at moderate load and
→ 1 at both very light and very heavy load** (light: nobody congests anyone; heavy: every route is saturated, so
there is little room to do better). Quoting one cherry-picked figure is therefore misleading. The benchmark
reports a sweep over `v/c ∈ {0.5, 0.8, 1.0, 1.2}`, with the resume number quoted *with its demand level*
(`project-spec.md §4`, `§R3`, `§R5`). (Classic reference: the Pigou example gives a PoA of `4/3`,
`project-spec.md §1`.)

## 5. Convergence criteria (planned, Phase 3)

For the iterative strategies (`project-spec.md §R5`), documented here as the *planned* criteria — these
algorithms are not yet built:

- **MSA** (Method of Successive Averages) and **SO** iterate until **relative gap `< 1e-4` or 100 iterations**,
  and report the achieved gap.
- **MSA step size** = `1/k` at iteration `k`. (MSA, not Frank-Wolfe.)

### 5.1 The `AssignResult` shape (Phase-3 foundations, issue #71)

The achieved gap, the iteration count, and the final per-edge flows have a single home: `AssignResult`
(`engine/internal/routing/routing.go`), the batch return shape settled once before any iterative router is
written so all five never have to be re-touched. The `Router` port carries both `Assign` (paths-only, the
backward-compatible face) and `AssignResult` (the full outcome); a router implements `AssignResult` and
defines `Assign` as `AssignFromResult` over it.

```go
type AssignResult struct {
	Routes     []Route   // chosen path per request, input order
	FinalFlows []float64 // dense per-edge flow (vehicles/hour), indexed by EdgeID, length EdgeCount()
	Gap        float64   // achieved relative convergence gap (§R5)
	Iters      int       // assignment iterations performed
	Converged  bool      // reached the convergence criterion within budget
}
```

- **`FinalFlows`** is `Σ (route traverses edge e ? 1 : 0) × request.Weight`, summed over all routes — the
  vector the Phase-4 benchmark applies BPR to for *realized* travel time and PoA (§3.2). It is sized to
  `EdgeCount()`, so an empty batch still yields a full-length all-zero vector.
- **Single-pass routers** (`naive`, `reactive`) report `Iters = 1`, `Gap = 0`, `Converged = true`: one
  assignment IS their result — though `reactive`'s is a herding best-response, not a UE (§3.1).
- **Reproducibility scaffolding** lives alongside it: a single-seed RNG (`NewSeededRNG`), sorted
  node/edge iteration helpers (Go randomizes map order, so the assignment path never ranges a map), and
  OD-set serialize/deserialize (`WriteODSet`/`ReadODSet`). A fixed seed plus a serialized OD set reproduces
  an `Assign` byte-for-byte. The per-goroutine Dijkstra scratch buffer keeps the iterative hot path from
  re-allocating `dist`/`prevEdge` per call.
