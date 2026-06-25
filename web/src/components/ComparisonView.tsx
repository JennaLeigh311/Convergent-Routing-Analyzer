// ComparisonView — the six-up small-multiples comparison (#101). It renders all six
// RouterOrder algorithms' live congestion side by side over the ONE shared /graph
// geometry, plus the per-algorithm metrics table below them. The geometry is built
// once (in useGraph) and passed by reference to every MiniCongestionMap, so the six
// panels share a single immutable geometry table and differ only in their live
// coloring. This component holds no per-frame state itself — each child subscribes to
// its own store slice.
//
// It is wrapped in React.memo because App still subscribes to the selected algo's
// slices (for the single view) and so re-renders every tick; without memo that would
// cascade through here and re-render all six panels on every delta. Its only prop,
// `geometry`, is a stable reference for the app's life (useGraph sets it once), so the
// memo cleanly severs that cascade — each panel then re-renders solely via its own
// narrow subscription, which is the intended per-panel isolation.
//
// Known tradeoff: the six panels are six independent <DeckGL> instances, i.e. six live
// WebGL contexts. That is well within the browser's ~16-context budget for this fixed
// 3×2 grid, but it does mean the shared geometry is uploaded once per context.

import { memo } from "react";

import { ROUTER_ORDER } from "../lib/protocol";
import type { GraphGeometry } from "../lib/graph";
import { AlgoMetricsTable } from "./AlgoMetricsTable";
import { MiniCongestionMap } from "./MiniCongestionMap";

interface Props {
  geometry: GraphGeometry;
}

export const ComparisonView = memo(function ComparisonView({ geometry }: Props) {
  return (
    <div className="comparison">
      <div className="comparison-maps">
        {ROUTER_ORDER.map((algo) => (
          <MiniCongestionMap key={algo} geometry={geometry} algo={algo} />
        ))}
      </div>
      <AlgoMetricsTable />
    </div>
  );
});
