// The pure congestion-state reducer: the wall-clock-free, side-effect-free core of
// the live map's data model. It maintains, per algorithm, a Map<segment_id, bucket>
// and applies snapshot / delta frames to it. Keeping this pure (no React, no socket,
// no DOM) is what lets us unit-test the delta-correctness invariant directly: a
// snapshot followed by every delta in order reproduces the full bucketed state the
// engine computed (engine/internal/api/stream.go's "delta-correctness invariant").
//
// The reducer is the single source of truth the deck.gl layer reads from to color
// each segment. It NEVER touches geometry — geometry comes from /graph and is joined
// to these buckets by segment_id at render time, so a recolor is a Map update, not a
// geometry rebuild.

import type { Algo, DeltaFrame, SnapshotFrame, StreamFrame } from "./protocol";
import { ROUTER_ORDER } from "./protocol";

/** Per-algorithm bucketed congestion: segment_id -> v/c bucket index. */
export type BucketMap = Map<string, number>;

/** The full live state: one BucketMap per algorithm, plus the latest metrics/tick. */
export interface CongestionState {
  /** segment_id -> bucket, per algorithm. */
  buckets: Record<Algo, BucketMap>;
  /** The latest tick seen per algorithm (0 = none yet). */
  tick: Record<Algo, number>;
  /** The latest sim_time string per algorithm ("" = none yet). */
  simTime: Record<Algo, string>;
  /** The latest metrics per algorithm (null = none yet). */
  metrics: Record<Algo, SnapshotFrame["metrics"] | null>;
}

/** A fresh, empty state with an empty bucket map for each algorithm. */
export function emptyCongestionState(): CongestionState {
  const buckets = {} as Record<Algo, BucketMap>;
  const tick = {} as Record<Algo, number>;
  const simTime = {} as Record<Algo, string>;
  const metrics = {} as Record<Algo, SnapshotFrame["metrics"] | null>;
  for (const algo of ROUTER_ORDER) {
    buckets[algo] = new Map();
    tick[algo] = 0;
    simTime[algo] = "";
    metrics[algo] = null;
  }
  return { buckets, tick, simTime, metrics };
}

/**
 * Apply a snapshot frame: REPLACE the algorithm's bucket map with the full state.
 * A snapshot is the diff base every subsequent delta builds on.
 */
function applySnapshot(state: CongestionState, frame: SnapshotFrame): CongestionState {
  const next = new Map<string, number>();
  for (const seg of frame.segments) {
    next.set(seg.segment_id, seg.bucket);
  }
  return writeAlgo(state, frame.algo, next, frame.tick, frame.sim_time, frame.metrics);
}

/**
 * Apply a delta frame: MUTATE only the changed segments onto a copy of the
 * algorithm's current bucket map. Applying snapshot + all deltas in order
 * reproduces the engine's full bucketed state at every tick.
 */
function applyDelta(state: CongestionState, frame: DeltaFrame): CongestionState {
  const next = new Map(state.buckets[frame.algo]);
  for (const seg of frame.changed) {
    next.set(seg.segment_id, seg.bucket);
  }
  return writeAlgo(state, frame.algo, next, frame.tick, frame.sim_time, frame.metrics);
}

/** Write a single algorithm's slot, returning a new top-level state object. */
function writeAlgo(
  state: CongestionState,
  algo: Algo,
  next: BucketMap,
  tick: number,
  simTime: string,
  metrics: SnapshotFrame["metrics"],
): CongestionState {
  return {
    buckets: { ...state.buckets, [algo]: next },
    tick: { ...state.tick, [algo]: tick },
    simTime: { ...state.simTime, [algo]: simTime },
    metrics: { ...state.metrics, [algo]: metrics },
  };
}

/**
 * The reducer entry point: fold one stream frame into the state, returning a new
 * state (immutable at the top level so React can diff by reference). Unknown frame
 * types are a no-op (forward-compatible).
 */
export function reduceCongestion(state: CongestionState, frame: StreamFrame): CongestionState {
  switch (frame.type) {
    case "snapshot":
      return applySnapshot(state, frame);
    case "delta":
      return applyDelta(state, frame);
    default:
      return state;
  }
}
