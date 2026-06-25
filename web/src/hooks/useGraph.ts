// useGraph — fetch the static /graph geometry exactly ONCE and expose it. The
// geometry never changes for the life of the app (the road network is immutable
// server-side), so this loads on mount and holds the result; congestion is joined
// to it by segment_id at render time, never rebuilding geometry.

import { useEffect, useState } from "react";

import { loadGraph, type GraphGeometry } from "../lib/graph";

interface GraphLoad {
  geometry: GraphGeometry | null;
  loading: boolean;
  error: string | null;
}

export function useGraph(): GraphLoad {
  const [state, setState] = useState<GraphLoad>({
    geometry: null,
    loading: true,
    error: null,
  });

  useEffect(() => {
    const ctrl = new AbortController();
    let active = true;
    loadGraph(ctrl.signal)
      .then((geometry) => {
        if (active) setState({ geometry, loading: false, error: null });
      })
      .catch((err: unknown) => {
        if (!active || ctrl.signal.aborted) return;
        const message = err instanceof Error ? err.message : "failed to load /graph";
        setState({ geometry: null, loading: false, error: message });
      });
    return () => {
      active = false;
      ctrl.abort();
    };
  }, []);

  return state;
}
