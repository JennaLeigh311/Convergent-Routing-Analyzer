// Shared route-overlay palette for the before/after view (#102): one source of truth
// so the map overlays and the side-panel bar charts can never disagree on which color
// means which router. naive is the selfish "before", reactive the congestion-aware
// "after", systemoptimal the optimal reference.

import type { RGB } from "./colorRamp";

/** Router name -> [r, g, b] for deck.gl layers. systemoptimal matches var(--accent)
 * (#4ea1ff); deck.gl can't read the CSS var, so this literal must track it by hand. */
export const ROUTE_RGB: Record<string, RGB> = {
  naive: [231, 76, 60], // red    — selfish "before"
  reactive: [46, 204, 113], // green  — congestion-aware "after"
  systemoptimal: [78, 161, 255], // accent — optimal reference (== var(--accent))
};

/** Router name -> CSS color, derived from ROUTE_RGB so the two can never drift apart.
 * Used by the bar charts, slider legend, and other DOM swatches. */
export const ROUTE_CSS: Record<string, string> = Object.fromEntries(
  Object.entries(ROUTE_RGB).map(([router, [r, g, b]]) => [router, `rgb(${r}, ${g}, ${b})`]),
);

/** Fallback color for an unexpected router name. */
export const ROUTE_RGB_FALLBACK: RGB = [154, 163, 178];
