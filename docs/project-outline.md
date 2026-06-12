# What This Project Actually Is (Plain English)

> A non-technical companion to the rest of the docs. Read this first; keep it open as a
> glossary when the other documents start throwing jargon at you.

## The one-sentence version

This project builds the "brain" behind a smarter GPS navigator — one that, when a thousand
people all ask for directions at once, *spreads them across different roads* instead of
dumping everyone onto the same "fastest" highway and turning it into a parking lot.

## The problem it solves: "the convergent routing problem"

Imagine 1,000 people leave downtown for the airport at 8am. A normal GPS looks at the map,
sees the highway is fastest, and tells **all 1,000** to take it. The advice was correct for
one car — but once all 1,000 follow it, the highway jams and becomes the *slowest* route.
The GPS was right at the moment you asked, and catastrophically wrong by the time you arrived.

That self-inflicted traffic jam — caused by everyone being given the same "best" answer — is
the **convergent routing problem** ("convergent" = everyone converging onto one path). This
is a real problem that Google Maps, Waze, and Uber all have to solve. This project builds a
simplified-but-realistic version of the solution: a router that *knows* lots of people are
asking at the same time and deliberately splits them across several good-enough routes so the
**total** travel time for everyone is lower.

## How the system is shaped (the three big pieces)

Think of it as a factory with three stations:

1. **The data pipeline** — takes a firehose of raw GPS location pings (from a real dataset of
   taxi movements) and continuously boils it down into "how congested is each road right now."
2. **The routing engine** — the actual brain. It holds a map of the roads and runs the
   route-planning math. This is the core of the project, written in **Go**.
3. **The web visualization** — a map in your browser that colors roads by how jammed they are,
   so a human can *see* what the engine is doing.

A deliberate design choice runs through the whole thing: the brain is built so it can run and
be tested **on its own**, with fake/simulated traffic data, before any of the heavy data
machinery exists. This is so the routing engine could later be plugged into something like
Uber or Google independently of this particular dataset.

## Glossary — every recurring term, defined

### The domain (traffic & maps)

- **Road graph / network graph** — A map represented as math: **nodes** (intersections) joined
  by **edges** (road segments). "Graph" here is the computer-science meaning (dots and lines),
  not a chart. [Intro to graphs](https://en.wikipedia.org/wiki/Graph_(discrete_mathematics))
- **Edge / segment** — One stretch of road between two intersections. The thing that gets
  "congested."
- **Directed** — An edge knows which *way* you're traveling. A two-way street is really two
  edges (one each direction); a one-way street is one. Direction matters because traffic going
  one way can be jammed while the other way is clear.
- **Congestion** — How clogged a road is right now. The engine uses this to make a road "cost"
  more so it can steer people around it.
- **v/c ratio (volume/capacity)** — Cars currently on a road ÷ how many it can comfortably
  hold. ~0 = empty, ~1 = at capacity, >1 = jammed. This is the number the map colors roads by.
- **Travel time** — The real measure of success. The whole point is lowering the *average*
  (and total) travel time across everyone, even if any single person's route is slightly longer.

### The routing algorithms (the "which is best?" experiment)

A big goal is to **benchmark** several routing strategies against each other to prove which one
beats the traffic problem best. The names that keep appearing:

- **Dijkstra** — The classic, famous "find the shortest path" algorithm. The baseline everyone
  starts from. [Friendly explainer](https://www.freecodecamp.org/news/dijkstras-shortest-path-algorithm-visual-introduction/)
- **Naive / shortest-path** — Plain Dijkstra that ignores congestion. The "dumb GPS" that
  causes the problem. The thing to beat.
- **Congestion-weighted / re-weighting** — Dijkstra, but roads that are jammed are treated as
  "longer," so the router avoids them. "Re-weight" = change how expensive a road looks.
- **Reactive** — Responds to congestion *after* it shows up (chases the problem).
- **Incremental** — Adds the 1,000 drivers onto the map a few at a time, re-checking traffic as
  it goes, instead of all at once.
- **User equilibrium / MSA** — A state where no single driver could switch routes and get home
  faster — everyone's individually doing their selfish best. **MSA** ("Method of Successive
  Averages") is the repetitive averaging technique used to compute that balance point.
  [User equilibrium](https://en.wikipedia.org/wiki/Route_assignment)
- **System-optimal / marginal-cost** — The *opposite* goal: routes chosen to make the **whole
  network's** total time lowest, even if some individuals sacrifice. "Marginal cost" = also
  charging each driver for the slowdown they impose on everyone behind them.
- **Multi-path / split** — Explicitly chop the 1,000 requests into groups and send each group
  down a different good route.
- **BPR / BPR cost** — A standard traffic-engineering formula (Bureau of Public Roads) for "how
  much does this road slow down as it fills up." It turns a v/c ratio into a realistic travel time.
- **Convergence criteria** — The rule for deciding "the repeated calculation has settled down
  enough; stop looping."
- **Price of Anarchy** — A score for *how much worse* everyone-for-themselves (user equilibrium)
  is than the coordinated ideal (system-optimal). It quantifies the cost of selfish routing.
  [Price of Anarchy](https://en.wikipedia.org/wiki/Price_of_anarchy)
- **Benchmark / benchmarking harness** — The test rig that runs all these strategies on the same
  1,000 requests and measures who delivers the lowest travel times. The project's headline result.

### The data plumbing

- **GPS ping** — One "here I am" location reading from one vehicle at one moment. The raw fuel.
- **T-Drive dataset** — The real-world data being used: ~15 million GPS points from ~10,000
  Beijing taxis over a week. (Porto, a smaller taxi dataset, is used first to get things working.)
- **Apache Kafka** — A high-speed conveyor belt for data. Producers drop messages on a **topic**
  (a named channel) and consumers pick them up. Used here to stream the GPS pings in.
  [Kafka in plain terms](https://www.confluent.io/what-is-apache-kafka/)
- **Apache Spark / Spark Structured Streaming** — The heavy-duty number cruncher that reads the
  ping stream and continuously tallies congestion per road. "Structured Streaming" = Spark's mode
  for processing never-ending live data.
- **Map-matching / map-matcher** — Snapping a messy GPS dot (which lands *near* a road, not
  exactly on it) onto the actual road segment the car is really on, and figuring out its direction.
- **Windowed aggregation / windowing** — Summarizing the stream in time-buckets, e.g. "cars per
  road in each 1-minute window," instead of trying to summarize an infinite stream all at once.
- **Watermark** — Spark's rule for "GPS data older than X is too late to bother counting,"
  letting it close out a time-window and move on.
- **Replay / replay stream** — Feeding the historical taxi data through the system *as if* it
  were happening live right now, so the engine can be tested on realistic traffic.
- **OpenStreetMap (OSM)** — The free, public "Wikipedia of maps" the road network comes from.
- **OSM way** — OSM's term for a road (or a piece of one). Its **way id** is a stable name for
  that road that everyone in the system can agree on.
- **pgRouting / osm2pgrouting** — Tools that load OpenStreetMap roads into a database in a form
  that's ready for route-finding. They chop long roads at intersections into routable pieces.
- **PostGIS / PostgreSQL** — The database (PostgreSQL) plus its maps add-on (PostGIS) that stores
  the road network.

### The `segment_id` contract (the project obsesses over this — here's why)

- **segment_id** — The **one agreed-upon name for a directed piece of road**, shared by the
  database, the data pipeline, and the engine. Format: `wayid:sequence:direction`, e.g.
  `"123456789:0:F"` (`F` = forward, `R` = reverse). If the three systems disagreed on road names,
  congestion would get attached to the wrong road and *every result would be garbage* — which is
  why this is called the most important **frozen contract** in the project.
- **Contract / frozen contract** — A shared agreement between teams about an exact data format,
  locked down ("frozen") so nobody changes it unilaterally and breaks everyone else. Changing it
  requires "bumping the version" and telling all teams.
- **Wire key / "on the wire"** — The identifier used when data travels *between* systems (over
  Kafka, in the database export, in the API). "On the wire" = in transit between components.
- **EdgeID** — A short internal number (0, 1, 2, …) the engine assigns to each road *inside its
  own memory* for speed. It must **never** leave the engine — `segment_id` is for travel between
  systems, `EdgeID` is for fast work inside one. (`gid`/`id` from the database is **banned** as a
  shared name because it changes every time the data is reloaded — unreliable.)
- **Golden fixtures** — A shared set of example inputs and their correct outputs, stored in files
  both the Go and the Python code load, so the two implementations can't silently drift apart.
- **Round-trip** — Confirming that converting a value *out* and back *in* gives you the original
  unchanged — a basic correctness check for the segment_id format.

### How the code is organized (architecture words)

- **Ports-and-adapters (a.k.a. hexagonal architecture)** — A way of building software where the
  core logic talks only to **ports** (generic plug sockets / interfaces like "give me congestion
  data") and never to the specific machinery behind them. The real implementations are
  **adapters** (e.g. "get congestion from Kafka" vs. "get congestion from a fake test source").
  This is what lets the routing brain run against *simulated* traffic with no Kafka in sight.
  [Hexagonal architecture](https://en.wikipedia.org/wiki/Hexagonal_architecture_(software))
- **The four core ports** — the four plug-sockets the engine defines: **Graph** (the map),
  **CongestionProvider** (current traffic), **CostFunction** (how to price a road), **Router**
  (the route-finder).
- **Interface** — In code, a list of capabilities ("anything that can do X, Y, Z") without saying
  *how*. The "port" is an interface.
- **Domain** — The pure ideas of the problem (roads, IDs, directions) kept separate from any
  technology. Lives in a corner of the code that depends on nothing else.
- **Decoupling** — Keeping pieces independent so one can change (or be swapped for a fake) without
  forcing the others to change. The project repeatedly insists on decoupling the data side from
  the engine.
- **Profile / core profile** — A preset bundle of components to start up. The "core" profile runs
  just the engine + web + a fake traffic simulator (no heavy data tools), for easy local testing.
- **Polyglot repo** — One project folder containing multiple programming languages (Go + Python +
  JavaScript), each kept in its own area.
- **Structured logging / slog** — Recording diagnostic messages as tidy labeled fields
  (`requests=1000 algo=naive`) instead of loose sentences, so they're searchable later. **PII**
  (personally identifiable information) like raw GPS coordinates is deliberately *never* logged.

### Tools & tech names you'll see

- **Go (golang)** — The programming language the routing engine is written in. Fast, good at
  doing many things at once (important for handling 1,000 simultaneous requests).
- **React** — The JavaScript toolkit for building the interactive web map.
- **deck.gl / Leaflet** — Libraries for drawing maps and data overlays in the browser.
- **Docker / Docker Compose** — A way to package each component in a self-contained box and start
  the whole stack with one command, so it runs the same on any machine.
- **REST API** — The standard way an outside app would send a "route me from A to B" request to
  the engine over the web.
- **Phase 0 / Foundations** — The current early stage: setting up structure, contracts, and
  scaffolding before the real features are built. (The repo is in Phase 0 right now.)

## If you remember only three things

1. **The villain is everyone getting the same "best" route** — that's what creates the jam. The
   project spreads people out instead.
2. **`segment_id` is sacred** — it's the shared name for a road that three separate systems must
   agree on, or all the numbers are meaningless.
3. **The brain is built to run alone** — it talks to generic "ports," so it can be tested with
   fake traffic and later plugged into any real data source.
