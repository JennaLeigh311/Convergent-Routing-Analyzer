// AlgoSelector — pick which of the six RouterOrder algorithms the map renders.
// For issue #100 exactly one is shown at a time (the six-up comparison is #101).
// All six stream concurrently over the one socket, so switching is instant: it just
// points the map at a different already-populated bucket map.

import { ALGO_LABELS } from "../lib/algoLabels";
import { ROUTER_ORDER, type Algo } from "../lib/protocol";

interface Props {
  selected: Algo;
  onSelect: (algo: Algo) => void;
}

export function AlgoSelector({ selected, onSelect }: Props) {
  return (
    <fieldset className="algo-selector">
      <legend>Algorithm</legend>
      {ROUTER_ORDER.map((algo) => (
        <label key={algo} className={algo === selected ? "selected" : ""}>
          <input
            type="radio"
            name="algo"
            value={algo}
            checked={algo === selected}
            onChange={() => onSelect(algo)}
          />
          {ALGO_LABELS[algo]}
        </label>
      ))}
    </fieldset>
  );
}
