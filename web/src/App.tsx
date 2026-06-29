// App — the simplified live congestion screen: load /graph geometry once, and let the
// user Start/Stop the analysis and pick the car load + algorithm. While running, all
// six algorithms fold concurrently into the store off one /stream socket; the map paints
// the selected one and a small summary card shows its headline metrics. There are no
// other views — Start, set the load, pick the algorithm, watch the roads congest.

import { useEffect } from "react";

import { CongestionMap } from "./components/CongestionMap";
import { Controls } from "./components/Controls";
import { MetricsPanel } from "./components/MetricsPanel";
import { Legend } from "./components/Legend";
import { useCongestionSocket } from "./hooks/useCongestionSocket";
import { useGraph } from "./hooks/useGraph";
import { useAppStore } from "./store";

const STATUS_LABEL: Record<string, string> = {
  idle: "stopped",
  connecting: "starting…",
  open: "running",
  closed: "finished",
  error: "error",
};

export default function App() {
  const { geometry, loading, error: graphError } = useGraph();

  const running = useAppStore((s) => s.running);
  const setRunning = useAppStore((s) => s.setRunning);
  const carLoad = useAppStore((s) => s.carLoad);
  const selectedAlgo = useAppStore((s) => s.selectedAlgo);
  const setSelectedAlgo = useAppStore((s) => s.setSelectedAlgo);
  const status = useAppStore((s) => s.status);
  const streamError = useAppStore((s) => s.error);

  // The selected algo's live state. Subscribing to just these slices means a delta for
  // a non-selected algorithm doesn't re-render the map or the summary card.
  const buckets = useAppStore((s) => s.congestion.buckets[selectedAlgo]);
  const metrics = useAppStore((s) => s.congestion.metrics[selectedAlgo]);

  // Open /stream only while running; reconnect when the car load changes (re-seeds the
  // simulation with the new request volume).
  useCongestionSocket({ enabled: running, count: carLoad, speed: 120, tickHz: 1 });

  // The simulation is finite: when the stream drains the server closes it. Flip the
  // run flag back off so the button returns to "Start" without the user pressing Stop.
  useEffect(() => {
    if (running && status === "closed") setRunning(false);
  }, [running, status, setRunning]);

  return (
    <div className="app">
      <header className="app-header">
        <h1>Traffic Routing Analyzer</h1>
        <span className={`status status-${status}`}>{STATUS_LABEL[status] ?? status}</span>
      </header>

      <aside className="sidebar">
        <Controls
          running={running}
          onToggle={() => setRunning(!running)}
          selectedAlgo={selectedAlgo}
          onSelectAlgo={setSelectedAlgo}
        />
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
        {geometry && (
          <>
            <CongestionMap geometry={geometry} buckets={buckets} />
            {metrics && <MetricsPanel metrics={metrics} />}
            {!running && !metrics && (
              <div className="hint-overlay">Press “Start analysis” to begin.</div>
            )}
          </>
        )}
      </main>
    </div>
  );
}
