// Zustand store: the app's UI + live-data state. The live congestion lives here as
// a CongestionState the socket hook folds frames into; the selected algorithm and
// connection status are plain UI state. Components subscribe to the slices they
// need so a delta that recolors the map doesn't re-render the algorithm selector.

import { create } from "zustand";

import {
  emptyCongestionState,
  reduceCongestion,
  type CongestionState,
} from "./lib/congestion";
import { DEFAULT_BENCH_PARAMS, type BenchmarkReport, type BenchmarkTuple } from "./lib/benchmark";
import type { Algo, StreamFrame } from "./lib/protocol";

export type ConnStatus = "idle" | "connecting" | "open" | "closed" | "error";

/** UI lifecycle of the async parameter benchmark (issue #104). */
export type BenchUiStatus = "idle" | "running" | "done" | "failed";

interface AppState {
  /** The algorithm whose congestion the map renders (issue #100: one at a time). */
  selectedAlgo: Algo;
  setSelectedAlgo: (algo: Algo) => void;

  /** Live per-algorithm bucketed congestion, folded from /stream frames. */
  congestion: CongestionState;
  /** Apply one stream frame (snapshot or delta) via the pure reducer. */
  applyFrame: (frame: StreamFrame) => void;
  /** Reset congestion to empty (on a fresh connection). */
  resetCongestion: () => void;

  /** WebSocket connection status, surfaced in the UI. */
  status: ConnStatus;
  setStatus: (status: ConnStatus) => void;
  /** Last connection error message, if any. */
  error: string | null;
  setError: (error: string | null) => void;

  // ---- Async parameter benchmark (issue #104) -------------------------------
  // A SEPARATE concern from the live-stream reducer above: these slices hold the
  // one-shot, §R6-parameterized /benchmark result, NOT the live congestion. The
  // controller (lib/benchmarkRunner) drives them via the actions below; components
  // subscribe to just the slice they render so a param change never re-renders the
  // live map.

  /** The §R6 tuple whose result is currently shown (or being computed). */
  benchParams: BenchmarkTuple;
  /** Cache key of the active tuple, so a stale run's resolve can't flip the view. */
  benchActiveKey: string | null;
  /** Pending-spinner vs done vs failed for the active tuple. */
  benchStatus: BenchUiStatus;
  /** The completed report for the active tuple, or null while pending/failed. */
  benchReport: BenchmarkReport | null;
  /** Client-safe error message for the active tuple, when it failed. */
  benchError: string | null;
  /** Results cached by §R6 tuple key — an identical tuple renders from here. */
  benchCache: Record<string, BenchmarkReport>;

  /** Mark a fresh tuple as running (spinner on, previous result/error cleared). */
  benchRequestStart: (key: string, params: BenchmarkTuple) => void;
  /** Cache a completed run; flip the view to done only if its key is still active. */
  benchResolve: (key: string, report: BenchmarkReport) => void;
  /** Surface a failure on the active tuple (ignored if it was superseded). */
  benchReject: (key: string, message: string) => void;
  /** Render a cached result for a tuple without firing a job. */
  benchUseCached: (key: string, report: BenchmarkReport, params: BenchmarkTuple) => void;
}

export const useAppStore = create<AppState>((set) => ({
  selectedAlgo: "reactive",
  setSelectedAlgo: (algo) => set({ selectedAlgo: algo }),

  congestion: emptyCongestionState(),
  applyFrame: (frame) =>
    set((s) => ({ congestion: reduceCongestion(s.congestion, frame) })),
  resetCongestion: () => set({ congestion: emptyCongestionState() }),

  status: "idle",
  setStatus: (status) => set({ status }),
  error: null,
  setError: (error) => set({ error }),

  benchParams: DEFAULT_BENCH_PARAMS,
  benchActiveKey: null,
  benchStatus: "idle",
  benchReport: null,
  benchError: null,
  benchCache: {},

  benchRequestStart: (key, params) =>
    set({
      benchActiveKey: key,
      benchParams: params,
      benchStatus: "running",
      benchReport: null,
      benchError: null,
    }),

  benchResolve: (key, report) =>
    set((s) => {
      // Always cache the completed report (even if a newer tuple superseded it).
      const benchCache = s.benchCache[key] ? s.benchCache : { ...s.benchCache, [key]: report };
      // Only flip the visible view when this key is still the active one.
      if (s.benchActiveKey !== key) return { benchCache };
      return { benchCache, benchReport: report, benchStatus: "done", benchError: null };
    }),

  benchReject: (key, message) =>
    set((s) => (s.benchActiveKey === key ? { benchStatus: "failed", benchError: message } : {})),

  benchUseCached: (key, report, params) =>
    set({
      benchActiveKey: key,
      benchParams: params,
      benchStatus: "done",
      benchReport: report,
      benchError: null,
    }),
}));
