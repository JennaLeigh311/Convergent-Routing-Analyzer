// Wire-protocol types for the engine's GET /graph (GeoJSON) and GET /stream
// (WebSocket snapshot + bucketed delta) endpoints. These mirror the authoritative
// shapes in docs/api.md and engine/internal/api/stream.go — kept narrow on purpose
// (only the fields the map consumes). The join key everywhere is `segment_id`,
// NEVER an internal EdgeID, and geometry is decoupled from congestion: /graph is
// static geometry, /stream is live bucketed v/c that the client joins by id.

/** The six canonical routers, in the engine's RouterOrder. */
export const ROUTER_ORDER = [
  "naive",
  "reactive",
  "incremental",
  "msa",
  "systemoptimal",
  "multipath",
] as const;

export type Algo = (typeof ROUTER_ORDER)[number];

/** v/c bucketing matches the engine: 24 buckets of width 0.1, bucket 23 saturates. */
export const VC_BUCKET_COUNT = 24;

// ---- GET /graph (GeoJSON FeatureCollection) ----------------------------------

export interface GraphFeature {
  type: "Feature";
  geometry: { type: "LineString"; coordinates: [number, number][] }; // [lon, lat]
  properties: { segment_id: string };
}

export interface GraphFeatureCollection {
  type: "FeatureCollection";
  features: GraphFeature[];
}

// ---- GET /stream frames ------------------------------------------------------

/** One segment's bucketed congestion as carried on a snapshot/delta frame. */
export interface SegmentVC {
  segment_id: string;
  vc: number;
  bucket: number;
}

/** Per-algorithm running metrics carried on every frame. */
export interface AlgoMetrics {
  /** Cumulative ms in router.Route — kept for back-compat; jittery. Prefer route_median_ns. */
  compute_ms: number;
  /** Median ns of a single router.Route call — the fair, stable "fastest to route" metric (#112). */
  route_median_ns: number;
  realized_total_s: number;
  /** Constant static-equilibrium Price of Anarchy vs. systemoptimal (matches /benchmark, #112). */
  poa: number;
  in_flight: number;
  completed: number;
}

/** Full bucketed state for an algorithm at its first tick. */
export interface SnapshotFrame {
  type: "snapshot";
  algo: Algo;
  tick: number;
  sim_time: string;
  segments: SegmentVC[];
  metrics: AlgoMetrics;
}

/** Only the segments whose bucket changed since the algorithm's last frame. */
export interface DeltaFrame {
  type: "delta";
  algo: Algo;
  tick: number;
  sim_time: string;
  changed: SegmentVC[];
  metrics: AlgoMetrics;
}

export type StreamFrame = SnapshotFrame | DeltaFrame;

/** Narrowing guard for an incoming parsed frame. */
export function isStreamFrame(x: unknown): x is StreamFrame {
  if (typeof x !== "object" || x === null) return false;
  const f = x as { type?: unknown; algo?: unknown; segments?: unknown; changed?: unknown };
  if (typeof f.algo !== "string" || !(ROUTER_ORDER as readonly string[]).includes(f.algo)) {
    return false;
  }
  // Validate the payload array too, so a well-typed-but-incomplete frame can't slip
  // past the guard and throw in the reducer's for…of (the reducer trusts the shape).
  if (f.type === "snapshot") return Array.isArray(f.segments);
  if (f.type === "delta") return Array.isArray(f.changed);
  return false;
}
