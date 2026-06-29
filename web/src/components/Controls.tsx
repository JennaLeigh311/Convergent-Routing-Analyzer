// Controls — the simplified control panel: a car-load slider, the algorithm picker,
// and one Start/Stop button. This is the whole of the user's input surface now.
//
// The car-load slider fires on EVERY drag tick, so the displayed number tracks the
// thumb live (local draft), but the committed value — which the live stream reconnects
// on — is debounced to the settled value (no change for DEBOUNCE_MS). That keeps a drag
// from reopening the socket on every step. A range input emits its final value on
// release as the last change event, so the settled value is exactly where you let go.

import { useEffect, useMemo, useState } from "react";

import { AlgoSelector } from "./AlgoSelector";
import { debounce } from "../lib/debounce";
import type { Algo } from "../lib/protocol";
import { useAppStore } from "../store";

/** Settle window before a released car-load slider commits (and the stream reconnects). */
const DEBOUNCE_MS = 350;

const CAR_LOAD_MIN = 100;
const CAR_LOAD_MAX = 5000;
const CAR_LOAD_STEP = 100;

interface Props {
  running: boolean;
  onToggle: () => void;
  selectedAlgo: Algo;
  onSelectAlgo: (algo: Algo) => void;
}

export function Controls({ running, onToggle, selectedAlgo, onSelectAlgo }: Props) {
  const carLoad = useAppStore((s) => s.carLoad);
  const setCarLoad = useAppStore((s) => s.setCarLoad);

  // Local draft so the number tracks the thumb during a drag; the committed value is
  // debounced into the store (which the socket query depends on).
  const [draft, setDraft] = useState(carLoad);

  // One debounced committer for the panel's life; cancel any pending commit on unmount.
  const commit = useMemo(
    () => debounce((v: number) => setCarLoad(v), DEBOUNCE_MS),
    [setCarLoad],
  );
  useEffect(() => commit.cancel, [commit]);

  function onCarLoad(v: number) {
    setDraft(v);
    commit(v);
  }

  return (
    <div className="controls">
      <label className="control-row">
        <span className="control-label">
          Car load
          <span className="control-value">{draft.toLocaleString()} vehicles</span>
        </span>
        <input
          type="range"
          min={CAR_LOAD_MIN}
          max={CAR_LOAD_MAX}
          step={CAR_LOAD_STEP}
          value={draft}
          onChange={(e) => onCarLoad(Number(e.target.value))}
        />
      </label>

      <AlgoSelector selected={selectedAlgo} onSelect={onSelectAlgo} />

      <button
        type="button"
        className={`run-button ${running ? "running" : ""}`}
        onClick={onToggle}
      >
        {running ? "■ Stop" : "▶ Start analysis"}
      </button>
    </div>
  );
}
