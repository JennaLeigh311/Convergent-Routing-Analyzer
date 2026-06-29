// useCongestionSocket — open the engine's GET /stream WebSocket and fold every
// snapshot/delta frame into the Zustand store via the pure reducer. Uses the
// browser-native WebSocket API. The hook owns the socket lifecycle: it opens when
// `enabled` is true (the user has pressed Start) and whenever the stream params
// change, resets the congestion state on a fresh connection so a reconnect re-seeds
// cleanly from the new snapshots, and closes on unmount or when disabled (Stop).
// When disabled it leaves the last frame's buckets in the store so the map stays
// painted. All six algorithms stream concurrently over the one socket; the store
// keeps a Map<segment_id,bucket> per algorithm and the map renders the selected one.

import { useEffect } from "react";

import { streamUrl } from "../lib/engine";
import { isStreamFrame } from "../lib/protocol";
import { useAppStore } from "../store";

/** The §R6 stream query knobs. Every field has an engine-side default. */
export interface StreamOptions {
  /** When false, the hook holds the socket closed (Stop). Defaults to true. */
  enabled?: boolean;
  start?: string; // RFC3339
  speed?: number; // simulated seconds per wall-clock second
  tickHz?: number; // server frame cadence
  seed?: number;
  count?: number;
  capScale?: number;
}

function buildQuery(opts: StreamOptions): string {
  const q = new URLSearchParams();
  if (opts.start) q.set("start", opts.start);
  if (opts.speed != null) q.set("speed", String(opts.speed));
  if (opts.tickHz != null) q.set("tick_hz", String(opts.tickHz));
  if (opts.seed != null) q.set("seed", String(opts.seed));
  if (opts.count != null) q.set("count", String(opts.count));
  if (opts.capScale != null) q.set("cap_scale", String(opts.capScale));
  return q.toString();
}

/**
 * Open /stream and stream frames into the store. The connection re-opens whenever
 * the serialized query changes (a new scenario). Returns nothing — state flows
 * through the store so any component can read it.
 */
export function useCongestionSocket(opts: StreamOptions = {}): void {
  const enabled = opts.enabled ?? true;
  const query = buildQuery(opts);

  useEffect(() => {
    const { applyFrame, resetCongestion, setStatus, setError } =
      useAppStore.getState();

    // Stopped: hold the socket closed and report idle. The last frame's buckets are
    // intentionally left in the store so the map stays painted while paused.
    if (!enabled) {
      setStatus("idle");
      return;
    }

    // A fresh connection re-seeds from new snapshots, so clear stale buckets.
    resetCongestion();
    setError(null);
    setStatus("connecting");

    const ws = new WebSocket(streamUrl(query));

    ws.onopen = () => setStatus("open");

    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") return; // frames are JSON text messages
      let parsed: unknown;
      try {
        parsed = JSON.parse(ev.data);
      } catch {
        return; // ignore a malformed frame rather than tearing down the stream
      }
      if (isStreamFrame(parsed)) {
        applyFrame(parsed);
      }
    };

    ws.onerror = () => {
      setStatus("error");
      setError("stream connection error");
    };

    // A clean server close (run drained) or any close lands here.
    ws.onclose = () => {
      // Only downgrade to "closed" if we weren't already flagged as an error.
      if (useAppStore.getState().status !== "error") {
        setStatus("closed");
      }
    };

    return () => {
      // Detach handlers before closing so a late event can't write stale state.
      ws.onopen = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onclose = null;
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
        ws.close();
      }
    };
  }, [enabled, query]);
}
