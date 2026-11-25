import type { Point } from "../api/types";

export interface Segment {
  from: number; // 0..1 along the window
  to: number;
  level: number; // 0..1 online; -1 = no data
}

/**
 * Turns history points into strip segments over [start, end]. Each point
 * covers the time until the next one; a gap longer than maxGap (or before
 * the first point) is "no data", so a stopped poller is not drawn as
 * offline. Hourly rollups (timestamped on the hour) cover their whole
 * hour and carry a fractional level.
 */
export function segments(points: Point[], start: number, end: number, maxGap = 15 * 60_000): Segment[] {
  const span = end - start;
  if (span <= 0) return [];
  const pts = points
    .map((p) => ({ t: Date.parse(p.ts), v: p.online }))
    .filter((p) => !Number.isNaN(p.t) && p.t < end)
    .sort((a, b) => a.t - b.t);
  const out: Segment[] = [];
  const push = (a: number, b: number, level: number) => {
    const from = Math.max(0, (a - start) / span);
    const to = Math.min(1, (b - start) / span);
    if (to <= from) return;
    const last = out[out.length - 1];
    if (last && last.level === level && Math.abs(last.to - from) < 1e-9) last.to = to;
    else out.push({ from, to, level });
  };
  let cursor = start;
  pts.forEach((p, i) => {
    const next = pts[i + 1];
    const reach = p.t % 3600_000 === 0 ? Math.max(maxGap, 3600_000) : maxGap;
    const covers = Math.min(next ? next.t : end, p.t + reach, end);
    if (p.t > cursor) push(cursor, p.t, -1);
    push(Math.max(p.t, start), covers, Math.max(0, Math.min(1, p.v)));
    cursor = Math.max(cursor, covers);
  });
  if (cursor < end) push(cursor, end, -1);
  return out;
}

/** Fraction of the known time spent online. */
export function uptime(segs: Segment[]): number | null {
  let known = 0;
  let on = 0;
  for (const s of segs) {
    if (s.level < 0) continue;
    known += s.to - s.from;
    on += (s.to - s.from) * s.level;
  }
  return known > 0 ? on / known : null;
}
