// App — wires the live congestion map together: load /graph geometry once, open the
// /stream socket, and render the selected algorithm's bucketed congestion over the
// static geometry. The algorithm selector switches which already-streaming algo the
// map paints; the metrics panel shows that algo's per-tick figures.

import { CongestionMap } from "./components/CongestionMap";
import { AlgoSelector } from "./components/AlgoSelector";
import { MetricsPanel } from "./components/MetricsPanel";
import { Legend } from "./components/Legend";
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

  const selectedAlgo = useAppStore((s) => s.selectedAlgo);
  const setSelectedAlgo = useAppStore((s) => s.setSelectedAlgo);
  const status = useAppStore((s) => s.status);
  const streamError = useAppStore((s) => s.error);

  // The selected algo's live state. Subscribing to just these slices means a delta
  // for a non-selected algorithm doesn't re-render the map.
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
        <AlgoSelector selected={selectedAlgo} onSelect={setSelectedAlgo} />
        <MetricsPanel tick={tick} simTime={simTime} metrics={metrics} />
        <Legend />
        {streamError && <p className="error">stream: {streamError}</p>}
      </aside>

      <main className="map-area">
        {loading && <div className="overlay">Loading network geometry…</div>}
        {graphError && (
          <div className="overlay error">
            Failed to load /graph: {graphError}
            <br />
            Is the engine running and reachable?
          </div>
        )}
        {geometry && <CongestionMap geometry={geometry} buckets={buckets} />}
      </main>
    </div>
  );
}
