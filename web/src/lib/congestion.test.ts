// Tests for the pure congestion reducer — chiefly the DELTA-CORRECTNESS INVARIANT
// the engine guarantees (engine/internal/api/stream.go): a snapshot followed by
// every delta in order reproduces the full bucketed state the engine computed at
// each tick. This mirrors the server-side stream_test.go assertion on the client.

import { describe, expect, it } from "vitest";

import {
  emptyCongestionState,
  reduceCongestion,
  type CongestionState,
} from "./congestion";
import type { AlgoMetrics, DeltaFrame, SnapshotFrame } from "./protocol";

const M: AlgoMetrics = {
  compute_ms: 0,
  realized_total_s: 0,
  poa: 1,
  in_flight: 0,
  completed: 0,
};

function snapshot(segs: Record<string, number>, tick = 1): SnapshotFrame {
  return {
    type: "snapshot",
    algo: "reactive",
    tick,
    sim_time: "2026-06-22T08:00:30Z",
    segments: Object.entries(segs).map(([segment_id, bucket]) => ({
      segment_id,
      vc: bucket * 0.1,
      bucket,
    })),
    metrics: M,
  };
}

function delta(changed: Record<string, number>, tick: number): DeltaFrame {
  return {
    type: "delta",
    algo: "reactive",
    tick,
    sim_time: `2026-06-22T08:0${tick}:30Z`,
    changed: Object.entries(changed).map(([segment_id, bucket]) => ({
      segment_id,
      vc: bucket * 0.1,
      bucket,
    })),
    metrics: M,
  };
}

/** The bucket map for an algorithm as a plain object, for easy comparison. */
function asObject(state: CongestionState): Record<string, number> {
  return Object.fromEntries(state.buckets.reactive);
}

describe("reduceCongestion", () => {
  it("seeds the bucket map from a snapshot", () => {
    const s = reduceCongestion(
      emptyCongestionState(),
      snapshot({ "a:0:F": 2, "b:0:F": 0, "c:0:F": 5 }),
    );
    expect(asObject(s)).toEqual({ "a:0:F": 2, "b:0:F": 0, "c:0:F": 5 });
    expect(s.tick.reactive).toBe(1);
    expect(s.metrics.reactive).toEqual(M);
  });

  it("delta-correctness: snapshot + deltas reproduce the full bucketed state", () => {
    // A reference state we mutate alongside the reducer so we can compare them.
    const full = new Map<string, number>([
      ["a:0:F", 1],
      ["b:0:F", 3],
      ["c:0:F", 7],
    ]);

    let state = reduceCongestion(
      emptyCongestionState(),
      snapshot(Object.fromEntries(full)),
    );
    expect(asObject(state)).toEqual(Object.fromEntries(full));

    // A sequence of deltas; each carries ONLY the segments whose bucket changed.
    const deltas: Array<Record<string, number>> = [
      { "a:0:F": 4 },
      { "b:0:F": 5, "c:0:F": 6 },
      { "a:0:F": 9 },
      { "c:0:F": 23 }, // saturating bucket
    ];

    deltas.forEach((changed, i) => {
      // Apply to the reference and to the reducer; they must stay in lockstep.
      for (const [seg, bucket] of Object.entries(changed)) full.set(seg, bucket);
      state = reduceCongestion(state, delta(changed, i + 2));
      expect(asObject(state)).toEqual(Object.fromEntries(full));
    });

    // Unchanged segments keep their snapshot bucket; changed ones reflect the last delta.
    expect(asObject(state)).toEqual({ "a:0:F": 9, "b:0:F": 5, "c:0:F": 23 });
  });

  it("introduces new segments via a delta", () => {
    let state = reduceCongestion(emptyCongestionState(), snapshot({ "a:0:F": 0 }));
    state = reduceCongestion(state, delta({ "new:0:F": 4 }, 2));
    expect(asObject(state)).toEqual({ "a:0:F": 0, "new:0:F": 4 });
  });

  it("keeps each algorithm's state independent", () => {
    let state = reduceCongestion(emptyCongestionState(), snapshot({ "a:0:F": 2 }));
    // naive's map is untouched by a reactive snapshot.
    expect(state.buckets.naive.size).toBe(0);
    state = reduceCongestion(state, {
      type: "snapshot",
      algo: "naive",
      tick: 1,
      sim_time: "2026-06-22T08:00:30Z",
      segments: [{ segment_id: "a:0:F", vc: 0.9, bucket: 9 }],
      metrics: M,
    });
    expect(state.buckets.naive.get("a:0:F")).toBe(9);
    expect(state.buckets.reactive.get("a:0:F")).toBe(2);
  });

  it("returns a new top-level state object (reference change) on each frame", () => {
    const a = emptyCongestionState();
    const b = reduceCongestion(a, snapshot({ "a:0:F": 1 }));
    expect(b).not.toBe(a);
    expect(b.buckets.reactive).not.toBe(a.buckets.reactive);
  });
});
