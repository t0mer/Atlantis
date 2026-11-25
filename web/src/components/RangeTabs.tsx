import type { Range } from "../api/hooks";
import { cx } from "./ui";

const labels: Record<Range, string> = { "24h": "24 hours", "7d": "7 days", "30d": "30 days" };

export function RangeTabs({ value, onChange }: { value: Range; onChange: (r: Range) => void }) {
  return (
    <div role="group" aria-label="Time range" className="inline-flex rounded-md border border-line bg-shoal p-0.5 text-sm">
      {(Object.keys(labels) as Range[]).map((r) => (
        <button
          key={r}
          type="button"
          aria-pressed={value === r}
          onClick={() => onChange(r)}
          className={cx("rounded px-2.5 py-1", value === r ? "bg-sea font-semibold" : "text-drift hover:text-abyss")}
        >
          {labels[r]}
        </button>
      ))}
    </div>
  );
}
