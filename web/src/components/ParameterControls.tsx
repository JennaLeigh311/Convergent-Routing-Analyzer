// ParameterControls — the #104 async parameter panel. Debounced sliders + an algorithm
// picker drive the §R6 tuple; on SETTLE (not mid-drag) the controller fires a benchmark
// job, the client caches results by tuple, and the spinner/result render off the store.
//
// The "fire on release, not mid-drag" rule is the debounce: every slider tick updates
// the LOCAL draft (so the number tracks the thumb live), but only a settled value
// (no change for DEBOUNCE_MS) is committed to the controller. The controller then
// dedupes identical tuples against the client cache, so an identical settle never
// re-fires a job. A range input emits its value on release as the final change event,
// so the settled value is exactly what the user let go on.

import { useEffect, useMemo, useState } from "react";

import { useBenchmarkControls } from "../hooks/useBenchmarkControls";
import { ALGO_LABELS } from "../lib/algoLabels";
import {
  BENCH_ALGO_OPTIONS,
  DEFAULT_BENCH_PARAMS,
  type BenchAlgo,
  type BenchmarkTuple,
} from "../lib/benchmark";
import { debounce } from "../lib/debounce";
import { useAppStore } from "../store";

/** Settle window before a released slider fires a job. */
const DEBOUNCE_MS = 350;

/** Picker labels: the six router labels plus the six-router sweep. */
const ALGO_OPTION_LABELS: Record<BenchAlgo, string> = {
  all: "All (six-router sweep)",
  ...ALGO_LABELS,
};

interface SliderRowProps {
  label: string;
  value: number;
  min: number;
  max: number;
  step: number;
  /** Render the live value (e.g. fixed decimals or an integer). */
  format: (v: number) => string;
  onChange: (v: number) => void;
}

function SliderRow({ label, value, min, max, step, format, onChange }: SliderRowProps) {
  return (
    <label className="param-row">
      <span className="param-name">{label}</span>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
      />
      <output className="param-value">{format(value)}</output>
    </label>
  );
}

const STATUS_HINT: Record<string, string> = {
  idle: "",
  running: "running…",
  done: "done",
  failed: "failed",
};

export function ParameterControls() {
  const controller = useBenchmarkControls();
  const [draft, setDraft] = useState<BenchmarkTuple>(DEFAULT_BENCH_PARAMS);
  const status = useAppStore((s) => s.benchStatus);

  // One debounced committer for the panel's life; rebuilt only if the controller does.
  const commit = useMemo(
    () => debounce((p: BenchmarkTuple) => controller.request(p), DEBOUNCE_MS),
    [controller],
  );

  // Fire the canonical default run once on mount so the panel isn't empty; cancel any
  // pending debounced commit on unmount so it never fires into a gone controller.
  useEffect(() => {
    controller.request(DEFAULT_BENCH_PARAMS);
    return () => commit.cancel();
  }, [controller, commit]);

  function update(patch: Partial<BenchmarkTuple>) {
    setDraft((prev) => {
      const next = { ...prev, ...patch };
      commit(next);
      return next;
    });
  }

  // The algorithm select fires immediately (it is a discrete choice, not a drag), but
  // still goes through the same debounced commit so a rapid pick settles to one job.
  const singleMode = draft.algorithm !== "all";

  return (
    <fieldset className="param-controls">
      <legend>
        Benchmark parameters
        {status !== "idle" && <span className={`param-status param-status-${status}`}>{STATUS_HINT[status]}</span>}
      </legend>

      <label className="param-row param-row-select">
        <span className="param-name">algorithm</span>
        <select
          value={draft.algorithm}
          onChange={(e) => update({ algorithm: e.target.value as BenchAlgo })}
        >
          {BENCH_ALGO_OPTIONS.map((algo) => (
            <option key={algo} value={algo}>
              {ALGO_OPTION_LABELS[algo]}
            </option>
          ))}
        </select>
      </label>

      <SliderRow
        label="alpha (α)"
        value={draft.alpha}
        min={0}
        max={1}
        step={0.01}
        format={(v) => v.toFixed(2)}
        onChange={(v) => update({ alpha: v })}
      />
      <SliderRow
        label="beta (β)"
        value={draft.beta}
        min={0}
        max={10}
        step={0.5}
        format={(v) => v.toFixed(1)}
        onChange={(v) => update({ beta: v })}
      />
      <SliderRow
        label="capacity scale"
        value={draft.capacity_scale}
        min={0.1}
        max={3}
        step={0.1}
        format={(v) => `${v.toFixed(1)}×`}
        onChange={(v) => update({ capacity_scale: v })}
      />
      <SliderRow
        label="request count"
        value={draft.request_count}
        min={0}
        max={5000}
        step={100}
        format={(v) => String(v)}
        onChange={(v) => update({ request_count: v })}
      />
      <SliderRow
        label="seed"
        value={draft.seed}
        min={0}
        max={50}
        step={1}
        format={(v) => String(v)}
        onChange={(v) => update({ seed: v })}
      />

      <p className="param-hint">
        {singleMode
          ? "α, β and capacity scale drive the BPR cost in single-algorithm mode."
          : "“All” runs the six-router sweep; it owns its own capacity axis, so α, β and capacity scale don’t change the grid (they stay part of the cache identity)."}
      </p>
    </fieldset>
  );
}
