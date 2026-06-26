// BeforeAfterView — the Price-of-Anarchy "money shot" (#102): the naive (selfish,
// "before") and reactive (congestion-aware, "after") routes for ONE OD pair drawn
// over the shared /graph geometry, with a blend slider that cross-fades the two
// overlays, plus the PoaPanel side panel (headline PoA + mean/p95/total bars).
//
// Geometry reuse mirrors the live map: the immutable segment_id -> path table built
// once in lib/graph is shared by the dim base layer and both route overlays — the
// overlays just join each route's ordered segment_ids back to that geometry
// (lib/compare.resolveRoutePath). The two overlays are separate PathLayers whose
// layer `opacity` is driven by the slider (blend 0 = all naive, 1 = all reactive), so
// the "swipe" is a recolor-only opacity change — never a geometry rebuild — exactly
// like the rest of the app.
//
// Wrapped in React.memo because App re-renders every stream tick (it subscribes to the
// selected algo's slices for the single view); geometry is a stable reference, so the
// memo severs that per-tick cascade and this view re-renders only on its own slider /
// fetch state.
//
// OD pair: derived from opposite corners of the network bounds (the longest-diagonal
// demo route) so the money shot renders with no user input; click-to-place endpoints
// is a deliberate non-goal here (a later nicety) — the slider is the required control.

import { memo, useMemo, useState } from "react";
import DeckGL from "@deck.gl/react";
import { PathLayer, ScatterplotLayer } from "@deck.gl/layers";
import type { Color, Layer } from "@deck.gl/core";

import { initialViewState, type GraphGeometry } from "../lib/graph";
import {
  buildSegmentIndex,
  defaultOD,
  resolveRoutePath,
  type Coordinate,
  type RoutePathSegment,
} from "../lib/compare";
import { ROUTE_CSS, ROUTE_RGB, ROUTE_RGB_FALLBACK } from "../lib/routeStyle";
import { useCompare } from "../hooks/useCompare";
import { useBenchmark } from "../hooks/useBenchmark";
import { PoaPanel } from "./PoaPanel";

interface Props {
  geometry: GraphGeometry;
}

const BASE_COLOR: Color = [42, 47, 61, 180]; // dim network context under the overlays

// The off-extreme opacity each route fades to (never fully invisible) so both routes
// stay faintly legible at the slider ends. The on-extreme route is at full opacity.
const ROUTE_OPACITY_FLOOR = 0.15;
const ROUTE_OPACITY_SPAN = 1 - ROUTE_OPACITY_FLOOR;

/** A solid route overlay PathLayer, faded by the slider via layer opacity. */
function routeLayer(id: string, router: string, paths: RoutePathSegment[], opacity: number): PathLayer<RoutePathSegment> {
  const [r, g, b] = ROUTE_RGB[router] ?? ROUTE_RGB_FALLBACK;
  return new PathLayer<RoutePathSegment>({
    id,
    data: paths,
    opacity,
    widthUnits: "pixels",
    getWidth: 5,
    capRounded: true,
    jointRounded: true,
    getPath: (d) => d.path,
    getColor: [r, g, b, 255] as Color,
  });
}

export const BeforeAfterView = memo(function BeforeAfterView({ geometry }: Props) {
  // blend in [0, 1]: 0 shows only the naive route, 1 only the reactive route.
  const [blend, setBlend] = useState(0.5);

  const view = useMemo(() => initialViewState(geometry), [geometry]);
  const segmentIndex = useMemo(() => buildSegmentIndex(geometry), [geometry]);
  const od = useMemo(() => defaultOD(geometry.bounds), [geometry.bounds]);

  const { data, loading, error } = useCompare(od);
  const benchmark = useBenchmark();

  const naivePaths = useMemo(
    () => (data ? resolveRoutePath(segmentIndex, data.naive.segments) : []),
    [segmentIndex, data],
  );
  const reactivePaths = useMemo(
    () => (data ? resolveRoutePath(segmentIndex, data.reactive.segments) : []),
    [segmentIndex, data],
  );

  // The dim base network and the OD endpoints never depend on the slider, so they are
  // memoized off geometry / data and excluded from the blend-driven memo below — making
  // "only the two route overlays change as the slider moves" literally true.
  const baseLayer = useMemo(
    () =>
      new PathLayer({
        id: "ba-base",
        data: geometry.segments,
        widthUnits: "pixels",
        getWidth: 1.5,
        getPath: (d) => d.path,
        getColor: BASE_COLOR,
      }),
    [geometry],
  );
  const endpointsLayer = useMemo(
    () =>
      data
        ? new ScatterplotLayer<Coordinate>({
            id: "ba-endpoints",
            data: [data.from, data.to],
            getPosition: (d) => [d.lon, d.lat],
            getRadius: 6,
            radiusUnits: "pixels",
            getFillColor: [230, 232, 238, 255] as Color,
            stroked: true,
            getLineColor: [11, 13, 18, 255] as Color,
            lineWidthUnits: "pixels",
            getLineWidth: 1.5,
          })
        : null,
    [data],
  );

  const layers = useMemo(() => {
    const out: Layer[] = [baseLayer];
    // naive fades out as blend -> 1; reactive fades in. A tiny floor keeps each route
    // faintly visible at the extremes so the contrast is always legible.
    out.push(routeLayer("ba-naive", "naive", naivePaths, 1 - blend * ROUTE_OPACITY_SPAN));
    out.push(routeLayer("ba-reactive", "reactive", reactivePaths, ROUTE_OPACITY_FLOOR + blend * ROUTE_OPACITY_SPAN));
    if (endpointsLayer) out.push(endpointsLayer);
    return out;
  }, [baseLayer, endpointsLayer, naivePaths, reactivePaths, blend]);

  return (
    <div className="beforeafter">
      <div className="beforeafter-map">
        <DeckGL
          initialViewState={view}
          controller={true}
          layers={layers}
          style={{ position: "absolute", inset: "0" }}
        />
        {loading && <div className="overlay">Routing the OD pair…</div>}
        {error && <div className="overlay error">/compare: {error}</div>}

        <div className="ba-slider">
          <div className="ba-slider-ends">
            <span className="ba-end" style={{ color: ROUTE_CSS.naive }}>naive (before)</span>
            <span className="ba-end" style={{ color: ROUTE_CSS.reactive }}>reactive (after)</span>
          </div>
          <input
            type="range"
            min={0}
            max={100}
            value={Math.round(blend * 100)}
            onChange={(e) => setBlend(Number(e.target.value) / 100)}
            aria-label="Blend between the naive and reactive routes"
          />
          {data && (
            <div className="ba-costs">
              <span>naive cost {data.naive.cost_s.toFixed(0)} s</span>
              <span>reactive cost {data.reactive.cost_s.toFixed(0)} s</span>
            </div>
          )}
        </div>
      </div>

      <aside className="beforeafter-panel">
        <PoaPanel report={benchmark.report} loading={benchmark.loading} error={benchmark.error} />
      </aside>
    </div>
  );
});
