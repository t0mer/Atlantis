import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useClients, useEvents, useNode, useNodes, type Range } from "../api/hooks";
import { NodeChart } from "../components/Charts";
import { PresenceStrip } from "../components/Presence";
import { RangeTabs } from "../components/RangeTabs";
import { Bars, Button, Empty, ErrorNote, OnlineLabel, Panel, StatusMark } from "../components/ui";
import { ago, backhaulLabel, bandLabel, eventText, rate } from "../lib/format";
import { useNames } from "../lib/names";

export function NodeDetail() {
  const id = useParams().id ?? ""; // already decoded by the router
  const [range, setRange] = useState<Range>("24h");
  const { data, error } = useNode(id, range);
  const { data: nodes = [] } = useNodes();
  const { data: clients = [] } = useClients();
  const { data: events = [] } = useEvents();
  const name = useNames();
  if (error) return <ErrorNote error={error} />;
  const n = data?.node;
  if (!n) return <p className="text-drift">Loading…</p>;
  const parent = nodes.find((x) => x.id === n.parent_id);
  const mine = clients.filter((c) => c.online && c.node_id === n.id).sort((a, b) => b.down_bps - a.down_bps);
  const nodeEvents = events.filter((e) => e.subject === n.id);
  const facts: [string, React.ReactNode][] = [
    ["Model", `${n.model}${n.hardware_ver ? ` (hardware ${n.hardware_ver})` : ""}`],
    ["Firmware", n.software_ver],
    ["IP address", n.ip],
    ["MAC address", n.mac],
    ["Role", n.master ? "Main unit" : "Satellite"],
    ["Backhaul", n.master ? "—" : `${backhaulLabel(n.backhaul?.type)}${parent ? ` to ${parent.nickname || parent.model}` : ""}`],
    ["First seen", new Date(n.first_seen).toLocaleDateString()],
    ["Last seen", n.online ? "now" : ago(n.last_seen)],
  ];
  return (
    <div className="space-y-6">
      <div>
        <Link to="/nodes" className="text-sm text-drift hover:text-abyss">
          Decos
        </Link>
        <div className="mt-1 flex flex-wrap items-center justify-between gap-3">
          <h1 dir="auto" className="text-xl font-semibold tracking-tight">
            {n.nickname || n.model}
          </h1>
          <div className="flex items-center gap-3">
            <OnlineLabel online={n.online} subject={n.id} />
            <Button disabled title="Rebooting from Atlantis is not available yet. Use the Deco app.">
              Restart
            </Button>
          </div>
        </div>
      </div>

      <Panel title="Online" action={<RangeTabs value={range} onChange={setRange} />}>
        <PresenceStrip points={data.history ?? []} events={nodeEvents} window={range} height={30} label={n.nickname || n.model} showAxis />
      </Panel>

      <div className="grid gap-6 lg:grid-cols-2">
        <Panel title="Connected clients over time">
          <NodeChart points={data.history ?? []} long={range !== "24h"} />
        </Panel>
        <Panel title="Details">
          <dl className="text-sm">
            {facts.map(([k, v]) => (
              <div key={k} className="flex justify-between gap-4 border-t border-line py-2 first:border-t-0">
                <dt className="text-drift">{k}</dt>
                <dd className="tnum text-right">{v || "—"}</dd>
              </div>
            ))}
            {!n.master &&
              Object.entries(n.backhaul?.signal ?? {}).map(([b, v]) => (
                <div key={b} className="flex justify-between gap-4 border-t border-line py-2">
                  <dt className="text-drift">{bandLabel(b)} backhaul signal</dt>
                  <dd>
                    <Bars level={v} />
                  </dd>
                </div>
              ))}
          </dl>
        </Panel>
      </div>

      <Panel title={`Connected now (${mine.length})`}>
        {mine.length ? (
          <ul className="divide-y divide-line text-sm">
            {mine.map((c) => (
              <li key={c.mac}>
                <Link to={`/clients/${encodeURIComponent(c.mac)}`} className="flex items-center gap-3 py-2 hover:underline">
                  <StatusMark online subject={c.mac} />
                  <span dir="auto" className="min-w-0 flex-1 truncate">
                    {c.display_name}
                  </span>
                  <span className="text-drift">{bandLabel(c.band, c.wired)}</span>
                  <span className="tnum w-24 text-right">{rate(c.down_bps)}</span>
                </Link>
              </li>
            ))}
          </ul>
        ) : (
          <Empty>No devices are connected to this Deco right now.</Empty>
        )}
      </Panel>

      {nodeEvents.length > 0 && (
        <Panel title="Recent changes">
          <ul className="space-y-1.5 text-sm">
            {nodeEvents.slice(0, 12).map((e) => (
              <li key={e.id} className="flex gap-3">
                <time className="tnum w-36 shrink-0 text-drift">{new Date(e.ts).toLocaleString([], { dateStyle: "short", timeStyle: "short" })}</time>
                <span>{eventText(e, name)}</span>
              </li>
            ))}
          </ul>
        </Panel>
      )}
    </div>
  );
}
