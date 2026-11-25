import { useMemo } from "react";
import type { DecoEvent, Point } from "../api/types";
import { segments, uptime } from "../lib/presence";
import { eventTone } from "../lib/format";

const WINDOWS = { "24h": 24 * 3600_000, "7d": 7 * 86400_000, "30d": 30 * 86400_000 } as const;
export type WindowKey = keyof typeof WINDOWS;

/**
 * The tide strip: one band per device over a window. Solid lagoon where
 * it was online (opacity for partial hours), a hatched line where it was
 * gone, blank where there is no data. Events sit on it as ticks.
 */
export function PresenceStrip({
  points,
  events = [],
  window = "24h",
  height = 22,
  label,
  showAxis = false,
}: {
  points: Point[];
  events?: DecoEvent[];
  window?: WindowKey;
  height?: number;
  label: string;
  showAxis?: boolean;
}) {
  const end = Date.now();
  const start = end - WINDOWS[window];
  const segs = useMemo(() => segments(points, start, end), [points, start, end]);
  const up = uptime(segs);
  const ticks = events
    .map((e) => ({ x: (Date.parse(e.ts) - start) / (end - start), e }))
    .filter((t) => t.x >= 0 && t.x <= 1);
  const desc = up === null ? `${label}: no data yet` : `${label}: online ${Math.round(up * 100)}% of the last ${window}`;
  return (
    <figure className="w-full">
      <svg role="img" aria-label={desc} viewBox={`0 0 1000 ${height}`} preserveAspectRatio="none" className="block w-full" style={{ height }}>
        <title>{desc}</title>
        <line x1="0" x2="1000" y1={height / 2} y2={height / 2} stroke="var(--line)" strokeWidth="1" vectorEffect="non-scaling-stroke" />
        {segs.map((s, i) =>
          s.level < 0 ? null : s.level === 0 ? (
            <line
              key={i}
              x1={s.from * 1000}
              x2={s.to * 1000}
              y1={height / 2}
              y2={height / 2}
              stroke="var(--drift)"
              strokeWidth="2"
              strokeDasharray="3 3"
              vectorEffect="non-scaling-stroke"
            />
          ) : (
            <rect
              key={i}
              x={s.from * 1000}
              width={Math.max(1, (s.to - s.from) * 1000)}
              y={2}
              height={height - 4}
              rx={2}
              fill="var(--lagoon)"
              opacity={0.35 + 0.65 * s.level}
            />
          ),
        )}
        {ticks.map(({ x, e }) => (
          <rect
            key={e.id}
            x={x * 1000 - 1}
            width={2}
            y={0}
            height={height}
            fill={eventTone(e.type) === "bad" ? "var(--flare)" : "var(--buoy)"}
          >
            <title>{e.type}</title>
          </rect>
        ))}
      </svg>
      {showAxis && (
        <figcaption className="tnum mt-1 flex justify-between text-xs text-drift">
          <span>{window === "24h" ? "24 h ago" : window === "7d" ? "7 days ago" : "30 days ago"}</span>
          <span>now</span>
        </figcaption>
      )}
    </figure>
  );
}

export { WINDOWS };
