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
import type { Algo, StreamFrame } from "./lib/protocol";

export type ConnStatus = "idle" | "connecting" | "open" | "closed" | "error";

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
}));
