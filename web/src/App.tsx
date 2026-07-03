// App — the live parallel-comparison screen (#114). On load the user sees ALL SIX
// algorithms running in parallel over the shared /graph geometry, with two live
// leaderboards naming (1) the algorithm that computes a route the quickest and (2) the
// one that best minimizes realized traffic — each re-ranking live as the sim runs.
//
// The geometry is loaded once; the single /stream socket folds all six algorithms into
// the store every frame (we NEVER open six sockets — one socket, six folds). The
// controls stay a simple Start/Stop + car-load + algorithm picker; the algorithm
// picker now just focuses (highlights) one tile in the grid rather than swapping a
// single map. Start, set the load, watch the six roads congest and the boards re-rank.

import { useEffect } from "react";

import { ComparisonView } from "./components/ComparisonView";
import { Controls } from "./components/Controls";
import { Legend } from "./components/Legend";
import { useCongestionSocket } from "./hooks/useCongestionSocket";
import { useGraph } from "./hooks/useGraph";
import { ROUTER_ORDER } from "./lib/protocol";
import { formatInstant } from "./lib/simStart";
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
  const simStart = useAppStore((s) => s.simStart);
  const selectedAlgo = useAppStore((s) => s.selectedAlgo);
  const setSelectedAlgo = useAppStore((s) => s.setSelectedAlgo);
  const status = useAppStore((s) => s.status);
  const streamError = useAppStore((s) => s.error);
  const simTime = useAppStore((s) => s.congestion.simTime);

  // Open /stream only while running; reconnect when the car load OR the start instant
  // changes (each re-seeds the simulation — a new request volume, or a new time of day
  // whose demand curve congests the roads differently, #111). One socket folds all six
  // algorithms into the store — the six-up grid reads them straight from there.
  useCongestionSocket({
    enabled: running,
    start: simStart,
    count: carLoad,
    speed: 120,
    tickHz: 1,
  });

  // The running sim clock: the selected algorithm's latest sim_time, falling back to any
  // populated algo (all six advance together, but the selected one may not have a frame
  // yet on the very first tick). Empty until the first snapshot lands.
  const simClock =
    simTime[selectedAlgo] || ROUTER_ORDER.map((a) => simTime[a]).find(Boolean) || "";

  // The run is over once the stream drains (server closes → "closed") or the connection
  // fails ("error"). Flip the run flag back off so the button returns to "Start" without
  // the user pressing Stop — and so an engine-down Start can't strand the button on "Stop".
  useEffect(() => {
    if (running && (status === "closed" || status === "error")) setRunning(false);
  }, [running, status, setRunning]);

  return (
    <div className="app">
      <header className="app-header">
        <h1>Traffic Routing Analyzer</h1>
        <div className="header-status">
          <span className="sim-clock" title="Simulated clock">
            {simClock ? formatInstant(simClock) : formatInstant(simStart)}
          </span>
          <span className={`status status-${status}`}>
            {STATUS_LABEL[status] ?? status}
          </span>
        </div>
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
            <ComparisonView geometry={geometry} />
            {!running && status === "idle" && (
              <div className="hint-overlay">Press “Start analysis” to begin.</div>
            )}
            {running && status === "connecting" && (
              <div className="overlay restarting">
                Restarting from {formatInstant(simStart)}…
              </div>
            )}
          </>
        )}
      </main>
    </div>
  );
}
