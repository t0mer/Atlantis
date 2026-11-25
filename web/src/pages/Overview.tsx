import { Link } from "react-router-dom";
import { useClients, useEvents, useNetwork, useNode, useNodes } from "../api/hooks";
import type { Client, DecoEvent, Node } from "../api/types";
import { MeshTree } from "../components/MeshTree";
import { PresenceStrip } from "../components/Presence";
import { Empty, Panel, StatusMark } from "../components/ui";
import { bandLabel, eventText, rate } from "../lib/format";
import { useNames } from "../lib/names";

function Headline() {
  const { data: net } = useNetwork();
  if (!net) return <p className="text-lg text-drift">Reading your mesh…</p>;
  const inet = net.internet_online;
  const { online, total } = net.nodes;
  const decos = online === total ? (total === 1 ? "Your Deco is online" : `All ${total} Decos are online`) : `${online} of ${total} Decos are online`;
  const n = net.clients.total;
  return (
    <p className="max-w-[40ch] text-2xl leading-snug font-semibold tracking-tight text-balance">
      <span className={inet === false ? "text-flare" : ""}>
        {inet === null ? "Internet status is unknown." : inet ? "Internet is up." : "Internet is down."}
      </span>{" "}
      <span className={online < total ? "text-buoy" : ""}>{decos}</span>, with {n} {n === 1 ? "device" : "devices"} connected.
    </p>
  );
}

function NodeTide({ node, events }: { node: Node; events: DecoEvent[] }) {
  const { data } = useNode(node.id, "24h");
  return (
    <div className="grid grid-cols-[minmax(0,9rem)_1fr] items-center gap-3">
      <Link to={`/nodes/${encodeURIComponent(node.id)}`} dir="auto" className="truncate text-sm hover:underline">
        {node.nickname || node.model}
      </Link>
      <PresenceStrip points={data?.history ?? []} events={events.filter((e) => e.subject === node.id)} label={node.nickname || node.model} />
    </div>
  );
}

function Busy({ clients }: { clients: Client[] }) {
  const top = clients
    .filter((c) => c.online)
    .sort((a, b) => b.down_bps + b.up_bps - (a.down_bps + a.up_bps))
    .slice(0, 8);
  if (!top.length) return <Empty>No devices are connected right now.</Empty>;
  return (
    <table className="w-full text-sm">
      <thead className="text-left text-xs text-drift">
        <tr>
          <th className="pb-2 font-normal">Device</th>
          <th className="hidden pb-2 font-normal sm:table-cell">Connection</th>
          <th className="pb-2 text-right font-normal">Down</th>
          <th className="pb-2 text-right font-normal">Up</th>
        </tr>
      </thead>
      <tbody>
        {top.map((c) => (
          <tr key={c.mac} className="border-t border-line">
            <td className="py-2">
              <Link to={`/clients/${encodeURIComponent(c.mac)}`} className="flex items-center gap-2 hover:underline">
                <StatusMark online subject={c.mac} />
                <span dir="auto" className="truncate">
                  {c.display_name}
                </span>
              </Link>
            </td>
            <td className="hidden py-2 text-drift sm:table-cell">{bandLabel(c.band, c.wired)}</td>
            <td className="tnum py-2 text-right">{rate(c.down_bps)}</td>
            <td className="tnum py-2 text-right text-drift">{rate(c.up_bps)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function Overview() {
  const { data: nodes = [] } = useNodes();
  const { data: clients = [] } = useClients();
  const { data: events = [] } = useEvents();
  const name = useNames();
  const dayAgo = Date.now() - 86400_000;
  const recent = events.filter((e) => Date.parse(e.ts) > dayAgo);
  return (
    <div className="space-y-6">
      <Headline />
      <div className="grid gap-6 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
        <Panel title="Mesh">{nodes.length ? <MeshTree nodes={nodes} /> : <Empty>No Decos found yet.</Empty>}</Panel>
        <Panel title="Last 24 hours">
          <div className="space-y-3">
            {nodes.map((n) => (
              <NodeTide key={n.id} node={n} events={recent} />
            ))}
            {nodes.length > 0 && (
              <div className="grid grid-cols-[minmax(0,9rem)_1fr] gap-3">
                <span />
                <div className="tnum flex justify-between text-xs text-drift">
                  <span>24 h ago</span>
                  <span>now</span>
                </div>
              </div>
            )}
          </div>
          <ul className="mt-4 space-y-1.5 border-t border-line pt-3 text-sm">
            {recent.slice(0, 5).map((e) => (
              <li key={e.id} className="flex gap-3">
                <time className="tnum w-16 shrink-0 whitespace-nowrap text-drift">{new Date(e.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</time>
                <span>{eventText(e, name)}</span>
              </li>
            ))}
            {!recent.length && <li className="text-drift">Nothing changed in the last 24 hours.</li>}
            {recent.length > 5 && (
              <li>
                <Link to="/events" className="text-lagoon hover:underline">
                  See all {recent.length} changes
                </Link>
              </li>
            )}
          </ul>
        </Panel>
      </div>
      <Panel
        title="Busiest right now"
        action={
          <Link to="/clients" className="text-sm text-lagoon hover:underline">
            All {clients.length} devices
          </Link>
        }
      >
        <Busy clients={clients} />
      </Panel>
    </div>
  );
}
