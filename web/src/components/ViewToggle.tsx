// ViewToggle — switch the app between the single-algorithm map (#100), the six-up
// comparison view (#101), and the before/after route-overlay PoA view (#102). Pure
// presentational control; the active view is owned by App so it can decide what to
// render in the sidebar and map area.

export type AppView = "single" | "compare" | "beforeafter" | "benchmark";

interface Props {
  view: AppView;
  onChange: (view: AppView) => void;
}

const VIEWS: { value: AppView; label: string }[] = [
  { value: "single", label: "Single" },
  { value: "compare", label: "Compare" },
  { value: "beforeafter", label: "Before/After" },
  { value: "benchmark", label: "Benchmark" },
];

export function ViewToggle({ view, onChange }: Props) {
  return (
    <div className="view-toggle" role="group" aria-label="View">
      {VIEWS.map(({ value, label }) => (
        <button
          key={value}
          type="button"
          className={value === view ? "active" : ""}
          aria-pressed={value === view}
          onClick={() => onChange(value)}
        >
          {label}
        </button>
      ))}
    </div>
  );
}
