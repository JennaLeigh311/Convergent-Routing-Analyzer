// useCompare — fetch GET /compare for an OD pair and expose the naive/reactive
// routes (#102). The before/after view derives a default OD from the network bounds
// and passes it here; re-fetches when the OD changes (so a future click-to-place
// just hands a new pair). Aborts the in-flight request on unmount or OD change so a
// late response never overwrites a newer one.

import { useEffect, useState } from "react";

import { loadCompare, type CompareResponse, type ODPair } from "../lib/compare";

interface CompareLoad {
  data: CompareResponse | null;
  loading: boolean;
  error: string | null;
}

export function useCompare(od: ODPair): CompareLoad {
  const [state, setState] = useState<CompareLoad>({ data: null, loading: true, error: null });

  // Re-run only when the OD coordinates actually change, not on every render.
  const odKey = `${od.from.lat},${od.from.lon}:${od.to.lat},${od.to.lon}`;

  useEffect(() => {
    const ctrl = new AbortController();
    let active = true;
    setState((s) => ({ ...s, loading: true, error: null }));
    loadCompare(od, ctrl.signal)
      .then((data) => {
        if (active) setState({ data, loading: false, error: null });
      })
      .catch((err: unknown) => {
        if (!active || ctrl.signal.aborted) return;
        const message = err instanceof Error ? err.message : "failed to load /compare";
        setState({ data: null, loading: false, error: message });
      });
    return () => {
      active = false;
      ctrl.abort();
    };
    // odKey captures the OD identity; od itself is a fresh object each render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [odKey]);

  return state;
}
