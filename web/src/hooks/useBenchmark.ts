// useBenchmark — run a /benchmark sweep and expose its report (#102). It starts the
// job and polls to completion via runBenchmark, aborting on unmount so a late poll
// never sets state on a gone component. The default (empty) params run the canonical
// six-router sweep, which is what the before/after PoA panel needs (it reads the
// naive-vs-systemoptimal per-level PoA the sweep produces).
//
// Issue #104 owns the debounced-slider + result-caching layer that will drive this
// with live params; this hook keeps a single fire-once run so #104 can replace the
// trigger without touching the underlying client.

import { useEffect, useState } from "react";

import { runBenchmark, type BenchmarkParams, type BenchmarkReport } from "../lib/benchmark";

interface BenchmarkLoad {
  report: BenchmarkReport | null;
  loading: boolean;
  error: string | null;
}

export function useBenchmark(params: BenchmarkParams = {}): BenchmarkLoad {
  const [state, setState] = useState<BenchmarkLoad>({ report: null, loading: true, error: null });

  // The param tuple identity; re-run only when it actually changes.
  const paramsKey = JSON.stringify(params);

  useEffect(() => {
    const ctrl = new AbortController();
    let active = true;
    setState((s) => ({ ...s, loading: true, error: null }));
    runBenchmark(JSON.parse(paramsKey) as BenchmarkParams, { signal: ctrl.signal })
      .then((report) => {
        if (active) setState({ report, loading: false, error: null });
      })
      .catch((err: unknown) => {
        if (!active || ctrl.signal.aborted) return;
        const message = err instanceof Error ? err.message : "benchmark failed";
        setState({ report: null, loading: false, error: message });
      });
    return () => {
      active = false;
      ctrl.abort();
    };
  }, [paramsKey]);

  return state;
}
