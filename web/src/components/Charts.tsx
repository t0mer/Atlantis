import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import type { Point } from "../api/types";
import { rate } from "../lib/format";
import { Empty } from "./ui";

const tick = { fill: "var(--drift)", fontSize: 12 };
const fmtTime = (ms: number, long: boolean) =>
  new Date(ms).toLocaleString([], long ? { month: "short", day: "numeric" } : { hour: "2-digit", minute: "2-digit" });

/** Download (filled) and upload (line) over time. */
export function ThroughputChart({ points, long }: { points: Point[]; long: boolean }) {
  // Zero rates are omitted from the JSON, so a missing field means 0.
  const data = points.map((p) => ({ t: Date.parse(p.ts), down: p.down_bps ?? 0, up: p.up_bps ?? 0 }));
  if (data.length < 2) return <Empty>Not enough samples yet for a chart.</Empty>;
  return (
    <div className="h-56" role="img" aria-label="Throughput over time">
      <ResponsiveContainer>
        <AreaChart data={data} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
          <CartesianGrid stroke="var(--line)" vertical={false} />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} tickFormatter={(v) => fmtTime(v, long)} tick={tick} stroke="var(--line)" />
          <YAxis tickFormatter={(v) => rate(v)} tick={tick} width={72} stroke="var(--line)" />
          <Tooltip
            labelFormatter={(v) => new Date(Number(v)).toLocaleString()}
            formatter={(v: number, k: string) => [rate(v), k === "down" ? "Download" : "Upload"]}
            contentStyle={{ background: "var(--shoal)", border: "1px solid var(--line)", borderRadius: 6, color: "var(--abyss)" }}
          />
          <Area type="monotone" dataKey="down" stroke="var(--lagoon)" fill="var(--lagoon)" fillOpacity={0.18} strokeWidth={1.5} isAnimationActive={false} />
          <Area type="monotone" dataKey="up" stroke="var(--drift)" fill="none" strokeWidth={1.5} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}

/** Clients attached to a node over time, with CPU/memory when the model reports them. */
export function NodeChart({ points, long }: { points: Point[]; long: boolean }) {
  const data = points.map((p) => ({ t: Date.parse(p.ts), clients: p.clients ?? 0 }));
  if (data.length < 2) return <Empty>Not enough samples yet for a chart.</Empty>;
  return (
    <div className="h-48" role="img" aria-label="Clients over time">
      <ResponsiveContainer>
        <AreaChart data={data} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
          <CartesianGrid stroke="var(--line)" vertical={false} />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} tickFormatter={(v) => fmtTime(v, long)} tick={tick} stroke="var(--line)" />
          <YAxis allowDecimals={false} tick={tick} width={32} stroke="var(--line)" />
          <Tooltip
            labelFormatter={(v) => new Date(Number(v)).toLocaleString()}
            formatter={(v: number) => [Math.round(v), "Clients"]}
            contentStyle={{ background: "var(--shoal)", border: "1px solid var(--line)", borderRadius: 6, color: "var(--abyss)" }}
          />
          <Area type="stepAfter" dataKey="clients" stroke="var(--lagoon)" fill="var(--lagoon)" fillOpacity={0.15} strokeWidth={1.5} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
