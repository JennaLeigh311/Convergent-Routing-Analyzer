// Shared route-overlay palette for the before/after view (#102): one source of truth
// so the map overlays and the side-panel bar charts can never disagree on which color
// means which router. naive is the selfish "before", reactive the congestion-aware
// "after", systemoptimal the optimal reference.

import type { RGB } from "./colorRamp";

/** Router name -> [r, g, b] for deck.gl layers. */
export const ROUTE_RGB: Record<string, RGB> = {
  naive: [231, 76, 60], // red    — selfish "before"
  reactive: [46, 204, 113], // green  — congestion-aware "after"
  systemoptimal: [78, 161, 255], // accent — optimal reference
};

/** Router name -> CSS color, for the bar charts / legend swatches. */
export const ROUTE_CSS: Record<string, string> = {
  naive: "rgb(231, 76, 60)",
  reactive: "rgb(46, 204, 113)",
  systemoptimal: "rgb(78, 161, 255)",
};

/** Fallback color for an unexpected router name. */
export const ROUTE_RGB_FALLBACK: RGB = [154, 163, 178];
