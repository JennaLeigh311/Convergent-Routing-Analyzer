// App — wires the live congestion UI together: load /graph geometry once, open the
// /stream socket (all six algos fold concurrently into the store), and render the
// single-algorithm map (#100), the six-up comparison view (#101), or the before/after
// route-overlay PoA view (#102). The view toggle switches between them; the single
// view's selector picks which already-streaming algo that map paints, the comparison
// view shows all six at once, and the before/after view fetches /compare + /benchmark
// on demand (independent of the live stream).

import { useState } from "react";

import { CongestionMap } from "./components/CongestionMap";
import { ComparisonView } from "./components/ComparisonView";
import { BeforeAfterView } from "./components/BeforeAfterView";
import { BenchmarkView } from "./components/BenchmarkView";
import { ParameterControls } from "./components/ParameterControls";
import { AlgoSelector } from "./components/AlgoSelector";
import { MetricsPanel } from "./components/MetricsPanel";
import { Legend } from "./components/Legend";
import { ViewToggle, type AppView } from "./components/ViewToggle";
import { useCongestionSocket } from "./hooks/useCongestionSocket";
import { useGraph } from "./hooks/useGraph";
import { useAppStore } from "./store";

const STATUS_LABEL: Record<string, string> = {
  idle: "idle",
  connecting: "connecting…",
  open: "live",
  closed: "stream ended",
  error: "error",
};

export default function App() {
  const { geometry, loading, error: graphError } = useGraph();

  // `view` is transient render-branch state local to App (no other component reads it),
  // so it stays in useState rather than the store — unlike `selectedAlgo`, which is
  // live-data-adjacent store state from #100 that the socket/store layer owns.
  const [view, setView] = useState<AppView>("single");

  const selectedAlgo = useAppStore((s) => s.selectedAlgo);
  const setSelectedAlgo = useAppStore((s) => s.setSelectedAlgo);
  const status = useAppStore((s) => s.status);
  const streamError = useAppStore((s) => s.error);

  // The selected algo's live state, for the single-algo view. Subscribing to just
  // these slices means a delta for a non-selected algorithm doesn't re-render it.
  const buckets = useAppStore((s) => s.congestion.buckets[selectedAlgo]);
  const tick = useAppStore((s) => s.congestion.tick[selectedAlgo]);
  const simTime = useAppStore((s) => s.congestion.simTime[selectedAlgo]);
  const metrics = useAppStore((s) => s.congestion.metrics[selectedAlgo]);

  // Open /stream with the canonical scenario defaults (engine fills the rest).
  useCongestionSocket({ speed: 120, tickHz: 1 });

  return (
    <div className="app">
      <header className="app-header">
        <h1>Convergent Routing Analyzer — Live Congestion</h1>
        <span className={`status status-${status}`}>{STATUS_LABEL[status] ?? status}</span>
      </header>

      <aside className="sidebar">
        <ViewToggle view={view} onChange={setView} />
        {view === "single" && (
          <>
            <AlgoSelector selected={selectedAlgo} onSelect={setSelectedAlgo} />
            <MetricsPanel tick={tick} simTime={simTime} metrics={metrics} />
          </>
        )}
        {view === "benchmark" && <ParameterControls />}
        <Legend />
        {streamError && <p className="error">stream: {streamError}</p>}
      </aside>

      <main className="map-area">
        {view === "benchmark" ? (
          // The benchmark view is independent of /graph geometry and the live stream —
          // it renders the async §R6 sweep result, not the map — so it bypasses the
          // geometry gate below.
          <BenchmarkView />
        ) : (
          <>
            {loading && <div className="overlay">Loading network geometry…</div>}
            {graphError && (
              <div className="overlay error">
                Failed to load /graph: {graphError}
                <br />
                Is the engine running and reachable?
              </div>
            )}
            {geometry &&
              (view === "single" ? (
                <CongestionMap geometry={geometry} buckets={buckets} />
              ) : view === "compare" ? (
                <ComparisonView geometry={geometry} />
              ) : (
                <BeforeAfterView geometry={geometry} />
              ))}
          </>
        )}
      </main>
    </div>
  );
}
