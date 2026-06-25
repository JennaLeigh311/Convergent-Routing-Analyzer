// Legend — the green -> red v/c ramp the map colors by, labeled at a few v/c marks
// so the color encoding is legible.

import { bucketColor } from "../lib/colorRamp";
import { VC_BUCKET_COUNT } from "../lib/protocol";

export function Legend() {
  const swatches = Array.from({ length: VC_BUCKET_COUNT }, (_, b) => {
    const [r, g, bl] = bucketColor(b);
    return `rgb(${r},${g},${bl})`;
  });
  const gradient = `linear-gradient(to right, ${swatches.join(",")})`;

  return (
    <div className="legend">
      <span className="legend-title">v/c</span>
      <div className="legend-bar" style={{ background: gradient }} />
      <div className="legend-ticks">
        <span>0</span>
        <span>1.0</span>
        <span>2.3+</span>
      </div>
    </div>
  );
}
