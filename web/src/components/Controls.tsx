// Controls — the simplified control panel: a start-instant picker (date + time-of-day),
// a car-load slider, the algorithm picker, and one Start/Stop button. This is the whole
// of the user's input surface now.
//
// Both sliders fire on EVERY drag tick, so the displayed value tracks the thumb live
// (local draft), but the committed value — which the live stream reconnects on — is
// debounced to the settled value (no change for DEBOUNCE_MS). That keeps a drag from
// reopening the socket on every step. A range input emits its final value on release as
// the last change event, so the settled value is exactly where you let go. The start
// picker edits a date + minutes-of-day and commits the combined RFC3339 instant that
// /stream's `start` knob wants — moving it (re)starts the parallel run from that moment.

import { useEffect, useMemo, useState } from "react";

import { AlgoSelector } from "./AlgoSelector";
import { debounce } from "../lib/debounce";
import type { Algo } from "../lib/protocol";
import {
  combineSimStart,
  formatInstant,
  formatMinutes,
  MINUTES_IN_DAY,
  splitSimStart,
} from "../lib/simStart";
import { useAppStore } from "../store";

/** Settle window before a released slider commits (and the stream reconnects). */
const DEBOUNCE_MS = 350;

const CAR_LOAD_MIN = 100;
const CAR_LOAD_MAX = 5000;
const CAR_LOAD_STEP = 100;

/** Time-of-day slider granularity: quarter-hour steps across the day. */
const TIME_STEP = 15;

interface Props {
  running: boolean;
  onToggle: () => void;
  selectedAlgo: Algo;
  onSelectAlgo: (algo: Algo) => void;
}

export function Controls({ running, onToggle, selectedAlgo, onSelectAlgo }: Props) {
  const carLoad = useAppStore((s) => s.carLoad);
  const setCarLoad = useAppStore((s) => s.setCarLoad);
  const simStart = useAppStore((s) => s.simStart);
  const setSimStart = useAppStore((s) => s.setSimStart);

  // Local draft so the number tracks the thumb during a drag; the committed value is
  // debounced into the store (which the socket query depends on).
  const [draft, setDraft] = useState(carLoad);

  // The start instant is edited as a date + minutes-of-day; seed the drafts once from
  // the store's RFC3339 value (lazy init, like the car-load draft) and combine them back
  // into a `start` instant on commit. This panel is the only writer of simStart, so the
  // drafts stay authoritative — no re-seed effect is needed.
  const [dateDraft, setDateDraft] = useState(() => splitSimStart(simStart).date);
  const [minutesDraft, setMinutesDraft] = useState(() => splitSimStart(simStart).minutes);

  // One debounced committer per store setter for the panel's life; cancel any pending
  // commit on unmount so a settled value never lands after the control is gone.
  const commit = useMemo(
    () => debounce((v: number) => setCarLoad(v), DEBOUNCE_MS),
    [setCarLoad],
  );
  const commitStart = useMemo(
    () => debounce((rfc: string) => setSimStart(rfc), DEBOUNCE_MS),
    [setSimStart],
  );
  useEffect(() => commit.cancel, [commit]);
  useEffect(() => commitStart.cancel, [commitStart]);

  function onCarLoad(v: number) {
    setDraft(v);
    commit(v);
  }

  function onDate(date: string) {
    // Native date inputs are user-clearable (value → ""); ignore that so we never commit
    // a year-less `T08:00:00Z` to /stream — the controlled input reverts to the last date.
    if (!date) return;
    setDateDraft(date);
    commitStart(combineSimStart(date, minutesDraft));
  }

  function onMinutes(minutes: number) {
    setMinutesDraft(minutes);
    commitStart(combineSimStart(dateDraft, minutes));
  }

  return (
    <div className="controls">
      <div className="control-row">
        <span className="control-label">
          Start instant
          <span className="control-value">
            {formatInstant(combineSimStart(dateDraft, minutesDraft))}
          </span>
        </span>
        <input
          type="date"
          className="control-date"
          aria-label="Start date"
          value={dateDraft}
          onChange={(e) => onDate(e.target.value)}
        />
        <input
          type="range"
          min={0}
          max={MINUTES_IN_DAY - 1}
          step={TIME_STEP}
          value={minutesDraft}
          aria-label="Time of day"
          aria-valuetext={formatMinutes(minutesDraft)}
          onChange={(e) => onMinutes(Number(e.target.value))}
        />
      </div>

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
