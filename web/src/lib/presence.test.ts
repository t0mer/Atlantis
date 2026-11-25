import { expect, test } from "vitest";
import { segments, uptime } from "./presence";

const t0 = Date.parse("2026-01-01T00:00:00Z");
const at = (min: number) => new Date(t0 + min * 60_000).toISOString();

test("samples cover until the next one and merge", () => {
  const s = segments(
    [
      { ts: at(0), online: 1 },
      { ts: at(30), online: 1 },
      { ts: at(60), online: 0 },
    ],
    t0,
    t0 + 120 * 60_000,
    60 * 60_000,
  );
  expect(s).toEqual([
    { from: 0, to: 0.5, level: 1 },
    { from: 0.5, to: 1, level: 0 },
  ]);
  expect(uptime(s)).toBe(0.5);
});

test("gaps and the time before the first sample are no data", () => {
  const s = segments([{ ts: at(61), online: 1 }], t0, t0 + 120 * 60_000, 15 * 60_000);
  expect(s[0]).toEqual({ from: 0, to: 61 / 120, level: -1 });
  expect(s[1]!.level).toBe(1);
  expect(s[2]!.level).toBe(-1);
});

test("no points: all unknown; uptime unknown", () => {
  const s = segments([], t0, t0 + 1000);
  expect(s).toEqual([{ from: 0, to: 1, level: -1 }]);
  expect(uptime(s)).toBeNull();
});

test("hour-aligned rollups cover their whole hour", () => {
  const s = segments(
    [
      { ts: at(0), online: 1 },
      { ts: at(60), online: 0.5 },
    ],
    t0,
    t0 + 120 * 60_000,
  );
  expect(s).toEqual([
    { from: 0, to: 0.5, level: 1 },
    { from: 0.5, to: 1, level: 0.5 },
  ]);
});
