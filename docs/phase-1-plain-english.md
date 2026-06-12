# Phase 1 — Plain-English Plan & Glossary

> A companion to `where-we-are-plain-english.md`. That file explains **what has been built so far** (Phase 0).
> This file explains **what Phase 1 is trying to accomplish**, what each piece of work does, how they fit
> together, and ends with a **glossary** of the new jargon this phase introduces. Keep it open as a decoder ring.
>
> *Untracked, like the other `*-plain-english.md` docs — a local reading aid, not part of the repo contract.*

---

## 1. The 30-second recap

Phase 0 built the **skeleton, plumbing, and house rules** — the empty rooms and the agreed-upon
shapes of the data that flows between them. **Phase 1 builds the first working organ of the brain:
a road map the computer can actually hold in memory, and the ability to find the fastest route from
A to B across it.**

No live traffic yet, no real city yet, no Kafka or Spark. Just: *load a small road network, and route
across it.* That sounds modest, but it's the foundation every later phase stands on — every routing
algorithm, every benchmark, every map color is computed against this graph.

---

## 2. What Phase 1 is trying to accomplish (the one-sentence goal)

> **Take the road-network file we agreed on in Phase 0, load it into the engine as a fast in-memory map,
> and route a trip from one point to another using plain shortest-path — proven by a test and a tiny
> command-line tool.**

Crucially, Phase 1 does this **with no data dependencies**: no database, no message bus. The engine is
developed against a small **hand-built toy network** so the whole routing core can be built and trusted
before any real city or live data exists. That's the "decoupling" principle in action — the brain works
on its own.

---

## 3. What we're building, piece by piece

Think of it as a short assembly line. Each item below is one GitHub issue (one focused deliverable, one
pull request). They're ordered roughly the way they get built.

**The map, in memory**
- **The road network as a graph (#23).** A road map is, to a computer, a **graph**: **nodes**
  (intersections) joined by **edges** (one-way stretches of road). We store it as flat lists indexed by a
  number, so "give me the roads leaving intersection #5" is instant. Once loaded it never changes, which
  means many requests can read it at the same time safely. *(This is the piece currently in review.)*
- **A tiny housekeeping addition (#21, done).** The map can now report **how many edges** it has — a
  one-line addition, but it lets later code set aside exactly the right amount of memory for per-road
  numbers (like congestion) without guessing.

**Finding your way onto the map**
- **"Which intersection is nearest to this dot?" (#24).** A trip is requested as two map pins
  (latitude/longitude), not as intersection numbers. So we need to snap each pin to the closest
  intersection. Doing that quickly over thousands of points needs a **k-d tree** (a spatial index — a
  way of organizing points so "nearest neighbor" is fast). This also needs **haversine** distance — the
  correct way to measure distance between two lat/lon points on a round planet.

**Getting the real map shape in, safely**
- **The loader (#25).** The code that reads the agreed road-network file (a **GeoJSON** file) and turns
  it into the in-memory graph. Its defining trait: it is **suspicious**. If the file is malformed — a
  road whose id doesn't match its name, a missing version stamp, coordinates in the wrong order — it
  **refuses the whole file with a clear error** rather than loading a half-broken map. ("Silent failure
  is the enemy": a wrong map that *looks* fine is worse than an honest crash.)
- **The fixture it reads (#22).** A small, real example of that GeoJSON file (owned by the database
  engineer), plus a collection of deliberately-broken versions used to prove the loader actually rejects
  bad input.
- **The toy network (#26).** A hand-built miniature city, written in the *same* file format as a real
  export, so "load the toy" and "load a real city" are the exact same code path. This is the shared test
  map every later phase routes over.

**The actual routing**
- **Shortest path + the naive router (#27).** The heart of Phase 1. A shared **Dijkstra** engine (the
  classic shortest-path algorithm) plus the **naive** router: it finds each trip's fastest route using
  **free-flow time** (how long each road takes with zero traffic). It's called "naive" because it ignores
  congestion — it's the honest *baseline* we'll later beat. When asked to route many trips at once, it
  just routes each one independently (no coordination yet — that's Phase 3).
- **A tiny command-line tool (#28).** Type in a start and end point; it prints the route and its cost.
  The visible proof that the whole chain works end to end.

**Keeping ourselves honest (supporting work, in parallel)**
- **A test for the live-traffic message format (#29).** Phase 0 froze the shape of the "this road is
  busy" message but never wrote a test that runs it. This adds one, so that format can't silently drift
  before the engine starts depending on it.
- **A health/metrics endpoint (#30).** The server should be able to report its internal numbers
  (`/metrics`) — a Phase-0 goal that slipped; closing it now.
- **Stricter automated checks (#31, #32).** Pin the code-style checker so it can't silently turn off, and
  make the automated build actually run the new command-line tool and the benchmark on the toy map — so a
  broken route fails the build instead of sneaking through.

---

## 4. How the pieces fit (the Phase 1 flow)

```
   toy-network.geojson  ──read by──▶  Loader (#25)  ──builds──▶  Graph in memory (#23)
   (or a real export)     (validate &                              nodes + edges,
        ▲                  reject if bad)                          "who borders whom"
        │                                                                │
   golden + broken                                          NearestNode (#24, k-d tree)
   fixtures (#22)                                            snaps your start/end pins
                                                             to the closest intersections
                                                                          │
                                                                          ▼
                                                   Dijkstra + naive router (#27)
                                                   finds the lowest free-flow-time path
                                                                          │
                                                                          ▼
                                                          CLI (#28): prints the route
```

Read it as: a road file is **validated and loaded** into an **in-memory graph**; a trip's start/end pins
are **snapped to nearest intersections**; **Dijkstra** finds the fastest path; the **CLI** prints it.

---

## 5. What "done" looks like (the Phase 1 deliverable)

> **You can run a small command that loads the toy road network and prints the fastest route between two
> points — and a unit test proves that route is actually the shortest one.** The build automatically runs
> that route on every change, so it can't silently break.

That's the runnable artifact. It sets up Phase 2 (add a congestion model and make routes react to traffic)
and Phase 3 (the six competing routing algorithms — the research core).

---

## 6. Two things we deliberately are NOT doing yet

- **A\*** (a faster shortest-path that uses a straight-line "as the crow flies" hint). It shares the
  Dijkstra core but is a speed optimization that only pays off on a real city map — **deferred to Phase 3.**
- **NearestEdge / map-matching** (snapping a moving GPS dot to the *road* it's on, with direction). That's
  only needed once real GPS pings flow — **deferred to Phase 7.** For now it's a labeled placeholder.

Building these now would be untested code carried for many phases. We stub them honestly instead.

---

## 7. Glossary — the new jargon this phase introduces

**Graph & map structure**
- **graph** — the computer's model of a road network: **nodes** connected by **edges**. Not a chart.
- **node** — an intersection (a vertex). Has a position (lat/lon).
- **edge** — one directed stretch of road between two nodes. A two-way street is *two* edges (one each
  way); a one-way is one. Carries its length, its free-flow time, and its capacity.
- **directed edge** — an edge that goes one way only (from a "from" node to a "to" node). Direction
  matters because congestion and travel differ by direction.
- **adjacency list** — the way we store the graph: for each node, the list of edges leaving it. Makes
  "what roads leave here?" instant — which is exactly what routing asks, over and over.
- **CSR (compressed sparse row)** — a compact, cache-friendly way to store that adjacency: two flat
  arrays instead of a list-of-lists. An implementation detail that keeps the graph fast and memory-tight.
- **dense ids / dense index** — nodes are numbered `0,1,2,…N-1` with no gaps, and likewise edges. This
  lets the engine use the number as a direct array slot (instant lookup, no searching) and size per-road
  arrays exactly.
- **immutable / read-only** — once the graph is built it never changes. That's what makes it safe for
  many requests to read it **concurrently** (at the same time) without locks or corruption.

**Routing & cost**
- **shortest path** — the lowest-total-cost route between two nodes. "Cost" here is time, not distance.
- **Dijkstra('s algorithm)** — the classic, correct algorithm for finding the shortest path in a graph
  with non-negative edge costs. The shared core all six future routing strategies reuse.
- **naive router** — the simplest strategy: shortest path on **free-flow time**, ignoring congestion. The
  honest **baseline** we measure improvements against.
- **free-flow time** — how long a road takes to traverse with *zero* traffic (length ÷ speed limit).
  Phase 1 routes purely on this; congestion enters in Phase 2.
- **cost / weight** — the number Dijkstra adds up along a path. In Phase 1 it's free-flow seconds.
- **Route vs Assign** — *Route* answers one trip; *Assign* answers many trips at once. For the naive
  router, Assign is just "route each one independently" — real coordination (so trips don't all pile onto
  the same road) is the Phase 3 work.
- **A\*** *(deferred to Phase 3)* — Dijkstra plus a straight-line distance hint to search fewer nodes;
  faster on big maps.

**Finding your way onto the map**
- **NearestNode** — given a lat/lon pin, find the closest intersection. Used to turn a trip's start/end
  pins into graph nodes.
- **k-d tree** — a spatial index (a tree that splits points by coordinate) that makes "nearest point"
  queries fast instead of checking every node.
- **haversine** — the formula for great-circle distance between two lat/lon points (accounts for the
  Earth's curvature). Getting this right matters: a wrong distance silently picks the wrong nearest node.
- **NearestEdge / map-matching** *(deferred to Phase 7)* — snapping a moving GPS dot to the actual road
  (and direction) it's traveling on. Needed only once real pings flow.

**The road file & loading it**
- **GeoJSON** — a standard text format for geographic shapes. Our road export is a GeoJSON
  **FeatureCollection**: a list of "features," each a road's geometry (a line) plus its properties (id,
  length, capacity, …).
- **FeatureCollection** — the top-level GeoJSON container holding all the road features and a version
  stamp.
- **loader / adapter** — the code that reads an external file/format and converts it into the engine's
  internal types. The engine's core never reads GeoJSON directly — only the loader does, so the format
  could be swapped without touching the brain.
- **validate-and-reject / fail-closed** — the loader's stance: check every contract rule on load, and if
  anything is wrong, **reject the entire file** with a clear error rather than loading part of it. The
  opposite of "log a warning and continue."
- **golden fixture / reject corpus** — a known-good example file (the *golden* fixture) used to prove the
  loader accepts valid input, plus a set of deliberately-broken files (the *reject corpus*) used to prove
  it rejects bad input.
- **toy network** — a tiny, hand-built road map in the real export format, used as the shared test map
  for all routing work before a real city is loaded.
- **schema_version** — a version stamp inside the file. The loader checks it and refuses a file stamped
  with a version it doesn't understand — the guard against silently misreading a future format.

**Why "naive" is the point (background)**
- **Price of Anarchy** — the gap between everyone selfishly taking their own fastest route (which creates
  jams) and the best *overall* outcome. The naive router built this phase *is* that selfish baseline; the
  whole project is about measuring and closing that gap in later phases.

---

*Written at the start of Phase 1, after #21 (EdgeCount) merged and #23 (the in-memory graph) entered
review. The remaining Phase 1 issues — #22, #24, #25, #26, #27, #28 plus the supporting #29/#30/#31/#32 —
are open on the board.*
