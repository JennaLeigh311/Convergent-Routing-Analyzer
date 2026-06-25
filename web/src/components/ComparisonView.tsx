// ComparisonView — the six-up small-multiples comparison (#101). It renders all six
// RouterOrder algorithms' live congestion side by side over the ONE shared /graph
// geometry, plus the per-algorithm metrics table below them. The geometry is built
// once (in useGraph) and passed by reference to every MiniCongestionMap, so the six
// panels share a single immutable geometry table and differ only in their live
// coloring. This component holds no per-frame state itself — each child subscribes to
// its own store slice — so it does not re-render on a delta.

import { ROUTER_ORDER } from "../lib/protocol";
import type { GraphGeometry } from "../lib/graph";
import { AlgoMetricsTable } from "./AlgoMetricsTable";
import { MiniCongestionMap } from "./MiniCongestionMap";

interface Props {
  geometry: GraphGeometry;
}

export function ComparisonView({ geometry }: Props) {
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
}
