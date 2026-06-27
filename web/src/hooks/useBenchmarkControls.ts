// useBenchmarkControls — the React handle on the #104 benchmark controller. It owns a
// single controller for the component's lifetime (created once, not per render) and
// aborts any in-flight run on unmount so a late poll never writes to a gone component.
//
// This is DISTINCT from the #102 useBenchmark hook (which runs one fire-once sweep for
// the before/after panel). This hook drives the debounced, cached, param-sliders flow:
// the controller dedupes identical tuples and cancels superseded runs; the store holds
// the result. Keeping the controller in a ref (not state) means firing a request never
// itself triggers a re-render — only the store slices the result lands in do.

import { useEffect, useRef } from "react";

import { createBenchmarkController, type BenchmarkController } from "../lib/benchmarkRunner";
import { useAppStore } from "../store";

export function useBenchmarkControls(): BenchmarkController {
  const controllerRef = useRef<BenchmarkController | null>(null);
  if (controllerRef.current === null) {
    controllerRef.current = createBenchmarkController(useAppStore);
  }

  useEffect(() => {
    const controller = controllerRef.current;
    return () => controller?.dispose();
  }, []);

  return controllerRef.current;
}
