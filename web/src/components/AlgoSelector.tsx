// AlgoSelector — pick which of the six RouterOrder algorithms the map renders.
// All six stream concurrently over the one socket, so switching is instant: it just
// points the map at a different already-populated bucket map. A compact dropdown
// keeps the simplified control panel small.

import { ALGO_LABELS } from "../lib/algoLabels";
import { ROUTER_ORDER, type Algo } from "../lib/protocol";

interface Props {
  selected: Algo;
  onSelect: (algo: Algo) => void;
}

export function AlgoSelector({ selected, onSelect }: Props) {
  return (
    <label className="control-row">
      <span className="control-label">Algorithm</span>
      <select
        className="control-select"
        value={selected}
        onChange={(e) => onSelect(e.target.value as Algo)}
      >
        {ROUTER_ORDER.map((algo) => (
          <option key={algo} value={algo}>
            {ALGO_LABELS[algo]}
          </option>
        ))}
      </select>
    </label>
  );
}
