import { useQuery } from "@tanstack/react-query";
import { Eye } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { useClients, useNodes } from "../api/hooks";
import type { Client, Point } from "../api/types";
import { PresenceStrip } from "../components/Presence";
import { Bars, Empty, Input, PageTitle, StatusMark, selectClass } from "../components/ui";
import { ago, bandLabel, networkLabel, rate } from "../lib/format";

function Spark({ mac, label }: { mac: string; label: string }) {
  const { data } = useQuery({
    queryKey: ["spark", mac],
    queryFn: () => api<{ history: Point[] | null }>(`/api/v1/clients/${encodeURIComponent(mac)}?range=24h`),
    staleTime: 5 * 60_000,
  });
  return <PresenceStrip points={data?.history ?? []} height={14} label={label} />;
}

type Filters = { q: string; state: string; node: string; conn: string; net: string; watched: boolean };

function matches(c: Client, f: Filters): boolean {
  if (f.state === "online" && !c.online) return false;
  if (f.state === "offline" && c.online) return false;
  if (f.node && c.node_id !== f.node) return false;
  if (f.net && c.network !== f.net) return false;
  if (f.watched && !c.watch) return false;
  if (f.conn === "wired" && !c.wired) return false;
  if (f.conn && f.conn !== "wired" && (c.wired || !c.band.startsWith(f.conn))) return false;
  if (f.q) {
    const hay = `${c.display_name} ${c.name} ${c.alias} ${c.mac} ${c.ip} ${c.tags.join(" ")}`.toLowerCase();
    if (!hay.includes(f.q.toLowerCase())) return false;
  }
  return true;
}

export function Clients() {
  const { data: clients = [], isLoading } = useClients();
  const { data: nodes = [] } = useNodes();
  const [params, setParams] = useSearchParams();
  const [sort, setSort] = useState<"name" | "down" | "seen">("name");
  const f: Filters = {
    q: params.get("q") ?? "",
    state: params.get("state") ?? "",
    node: params.get("node") ?? "",
    conn: params.get("conn") ?? "",
    net: params.get("net") ?? "",
    watched: params.get("watched") === "1",
  };
  const set = (k: keyof Filters, v: string | boolean) => {
    const p = new URLSearchParams(params);
    if (v === "" || v === false) p.delete(k);
    else p.set(k, v === true ? "1" : String(v));
    setParams(p, { replace: true });
  };
  const nodeName = useMemo(() => new Map(nodes.map((n) => [n.id, n.nickname || n.model])), [nodes]);
  const shown = clients
    .filter((c) => matches(c, f))
    .sort((a, b) =>
      sort === "down"
        ? b.down_bps - a.down_bps
        : sort === "seen"
          ? Date.parse(b.last_seen) - Date.parse(a.last_seen)
          : Number(b.online) - Number(a.online) || a.display_name.localeCompare(b.display_name),
    );
  const online = clients.filter((c) => c.online).length;
  const select = (k: keyof Filters, label: string, opts: [string, string][]) => (
    <label className="text-sm">
      <span className="sr-only">{label}</span>
      <select className={selectClass} value={String(f[k])} onChange={(e) => set(k, e.target.value)}>
        {opts.map(([v, t]) => (
          <option key={v} value={v}>
            {t}
          </option>
        ))}
      </select>
    </label>
  );
  return (
    <>
      <PageTitle aside={<p className="text-sm text-drift">{online} online of {clients.length} seen</p>}>Clients</PageTitle>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Input placeholder="Search name, MAC, IP or tag" value={f.q} onChange={(e) => set("q", e.target.value)} className="max-w-xs" aria-label="Search" />
        {select("state", "State", [["", "Online and offline"], ["online", "Online"], ["offline", "Offline"]])}
        {select("node", "Deco", [["", "All Decos"], ...nodes.map((n): [string, string] => [n.id, n.nickname || n.model])])}
        {select("conn", "Connection", [["", "Any connection"], ["wired", "Wired"], ["band2_4", "2.4 GHz"], ["band5", "5 GHz"], ["band6", "6 GHz"]])}
        {select("net", "Network", [["", "Any network"], ["main", "Main"], ["guest", "Guest"], ["iot", "IoT"]])}
        <label className="flex items-center gap-1.5 text-sm">
          <input type="checkbox" checked={f.watched} onChange={(e) => set("watched", e.target.checked)} /> Watched only
        </label>
        <label className="ml-auto flex items-center gap-2 text-sm">
          <span className="text-drift">Sort</span>
          <select className={selectClass} value={sort} onChange={(e) => setSort(e.target.value as typeof sort)}>
            <option value="name">by name</option>
            <option value="down">by download</option>
            <option value="seen">by last seen</option>
          </select>
        </label>
      </div>
      <div className="overflow-x-auto rounded-lg border border-line bg-shoal">
        <table className="w-full min-w-[640px] text-sm">
          <thead className="border-b border-line text-left text-xs text-drift">
            <tr>
              <th className="px-3 py-2 font-normal">Device</th>
              <th className="px-3 py-2 font-normal">Deco</th>
              <th className="px-3 py-2 font-normal">Connection</th>
              <th className="px-3 py-2 text-right font-normal">Down / up</th>
              <th className="hidden w-48 px-3 py-2 font-normal lg:table-cell">Last 24 h</th>
              <th className="px-3 py-2 text-right font-normal">Seen</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((c) => (
              <tr key={c.mac} className="border-t border-line first:border-t-0 hover:bg-sea">
                <td className="px-3 py-2">
                  <Link to={`/clients/${encodeURIComponent(c.mac)}`} className="flex items-center gap-2">
                    <StatusMark online={c.online} subject={c.mac} />
                    <span className="min-w-0">
                      <span dir="auto" className="flex items-center gap-1 truncate font-medium">
                        {c.display_name}
                        {c.watch && <Eye size={13} className="text-lagoon" aria-label="Watched" />}
                      </span>
                      <span className="tnum block text-xs text-drift">
                        {c.ip || c.mac}
                        {c.private_mac && " · private address"}
                      </span>
                    </span>
                  </Link>
                </td>
                <td className="px-3 py-2 text-drift" dir="auto">
                  {nodeName.get(c.node_id) ?? "—"}
                </td>
                <td className="px-3 py-2">
                  <span className="inline-flex items-center gap-2">
                    {bandLabel(c.band, c.wired)}
                    {!c.wired && c.online && c.signal_level > 0 && <Bars level={c.signal_level} />}
                    {c.network !== "main" && c.network && <span className="text-xs text-drift">{networkLabel(c.network)}</span>}
                  </span>
                </td>
                <td className="tnum px-3 py-2 text-right whitespace-nowrap">
                  {c.online ? (
                    <>
                      {rate(c.down_bps)} <span className="text-drift">/ {rate(c.up_bps)}</span>
                    </>
                  ) : (
                    <span className="text-drift">—</span>
                  )}
                </td>
                <td className="hidden px-3 py-2 lg:table-cell">
                  <Spark mac={c.mac} label={c.display_name} />
                </td>
                <td className="tnum px-3 py-2 text-right whitespace-nowrap text-drift">{c.online ? "now" : ago(c.last_seen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {!isLoading && !shown.length && (
          <Empty>{clients.length ? "No devices match these filters." : "No devices seen yet. They appear after the first poll."}</Empty>
        )}
      </div>
    </>
  );
}
