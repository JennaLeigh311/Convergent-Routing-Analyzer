// ComparisonView — the DEFAULT screen (#114): all six RouterOrder algorithms running
// in parallel in real time, with the two live leaderboards above them. It renders the
// six MiniCongestionMaps over the ONE shared /graph geometry (built once in useGraph
// and passed by reference to every tile, so the panels share a single immutable
// geometry table and differ only in their live coloring), and the Leaderboards panel
// that re-ranks the algorithms live.
//
// This component holds NO per-frame state itself — each child owns its own narrow
// store subscription (per-algo buckets for the maps, the metrics record for the
// boards). It is wrapped in React.memo so that if a parent re-renders for an unrelated
// reason (car load, run toggle) the whole six-up grid isn't rebuilt; its only prop,
// `geometry`, is stable for the app's life.
//
// Known tradeoff (inherited from the #101 small-multiples design): six independent
// <DeckGL> instances = six live WebGL contexts. That is well within the browser's
// ~16-context budget for this fixed 3×2 grid.

import { memo } from "react";

import { ROUTER_ORDER } from "../lib/protocol";
import type { GraphGeometry } from "../lib/graph";
import { Leaderboards } from "./Leaderboards";
import { MiniCongestionMap } from "./MiniCongestionMap";

interface Props {
  geometry: GraphGeometry;
}

export const ComparisonView = memo(function ComparisonView({ geometry }: Props) {
  return (
    <div className="comparison">
      <Leaderboards />
      <div className="comparison-maps">
        {ROUTER_ORDER.map((algo) => (
          <MiniCongestionMap key={algo} geometry={geometry} algo={algo} />
        ))}
      </div>
    </div>
  );
});
