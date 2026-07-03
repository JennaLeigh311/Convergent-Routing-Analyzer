// Zustand store: the app's UI + live-data state. The live congestion lives here as
// a CongestionState the socket hook folds frames into; the selected algorithm, the
// connection status, and the simple run controls (is the analysis running, and the
// current car load) are plain UI state. Components subscribe to the slices they need
// so a delta that recolors the map doesn't re-render the controls.

import { create } from "zustand";

import {
  emptyCongestionState,
  reduceCongestion,
  type CongestionState,
} from "./lib/congestion";
import type { Algo, StreamFrame } from "./lib/protocol";

export type ConnStatus = "idle" | "connecting" | "open" | "closed" | "error";

/** Default car load (simultaneous routing requests) the analysis starts at. */
export const DEFAULT_CAR_LOAD = 1000;

interface AppState {
  /** The focused algorithm: highlights its tile in the six-up grid (the algo picker). */
  selectedAlgo: Algo;
  setSelectedAlgo: (algo: Algo) => void;

  /** Whether the user has started the live analysis (drives the /stream socket). */
  running: boolean;
  setRunning: (running: boolean) => void;

  /** Car load: number of simultaneous routing requests the simulation drives. */
  carLoad: number;
  setCarLoad: (carLoad: number) => void;

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

  running: false,
  setRunning: (running) => set({ running }),

  carLoad: DEFAULT_CAR_LOAD,
  setCarLoad: (carLoad) => set({ carLoad }),

  congestion: emptyCongestionState(),
  applyFrame: (frame) =>
    set((s) => ({ congestion: reduceCongestion(s.congestion, frame) })),
  resetCongestion: () => set({ congestion: emptyCongestionState() }),

  status: "idle",
  setStatus: (status) => set({ status }),
  error: null,
  setError: (error) => set({ error }),
}));
