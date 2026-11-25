import { Link } from "react-router-dom";
import { useFirmware, useNetwork, useNodes } from "../api/hooks";
import type { Firmware, NetworkSummary, Node } from "../api/types";
import { Bars, Empty, OnlineLabel, PageTitle, cx } from "../components/ui";
import { backhaulLabel, bandLabel } from "../lib/format";

function upgradeFor(n: Node, fw: Firmware[]): Firmware | undefined {
  return fw.find((f) => f.upgrade_available && (f.node_ids?.includes(n.id) || (!f.node_ids?.length && f.model === n.model)));
}

function Facts({ items }: { items: [string, React.ReactNode][] }) {
  return (
    <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-3">
      {items.map(([k, v]) => (
        <div key={k}>
          <dt className="text-xs text-drift">{k}</dt>
          <dd className="tnum">{v || "—"}</dd>
        </div>
      ))}
    </dl>
  );
}

function NodePanel({ n, parent, fw, net }: { n: Node; parent?: Node; fw?: Firmware; net?: NetworkSummary }) {
  const signal = Object.entries(n.backhaul?.signal ?? {});
  return (
    <Link
      to={`/nodes/${encodeURIComponent(n.id)}`}
      className={cx("block rounded-lg border bg-shoal p-4 hover:border-lagoon", n.online ? "border-line" : "border-flare")}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 dir="auto" className={cx("font-semibold", n.master ? "text-lg" : "")}>
            {n.nickname || n.model}
          </h2>
          <p className="text-sm text-drift">
            {n.model}
            {n.master ? " · main unit" : parent ? ` · via ${parent.nickname || parent.model}` : ""}
          </p>
        </div>
        <OnlineLabel online={n.online} subject={n.id} />
      </div>
      <div className="mt-3">
        <Facts
          items={[
            ["Clients", n.clients],
            ["IP address", n.ip],
            ["Firmware", n.software_ver],
            ...(n.master
              ? ([
                  ["Internet", n.internet_online ? "Up" : n.internet_error || "Down"],
                  ["Public IP", net?.wan?.ip ?? ""],
                  ["Load", net?.performance ? `CPU ${Math.round(net.performance.cpu_usage * 100)}%, memory ${Math.round(net.performance.mem_usage * 100)}%` : ""],
                ] as [string, React.ReactNode][])
              : ([
                  ["Backhaul", backhaulLabel(n.backhaul?.type)],
                  [
                    "Signal",
                    signal.length ? (
                      <span className="inline-flex flex-wrap gap-3">
                        {signal.map(([b, v]) => (
                          <span key={b} className="inline-flex items-center gap-1.5">
                            <Bars level={v} label={`${bandLabel(b)} signal`} /> <span className="text-xs text-drift">{bandLabel(b)}</span>
                          </span>
                        ))}
                      </span>
                    ) : (
                      ""
                    ),
                  ],
                ] as [string, React.ReactNode][])),
          ]}
        />
      </div>
      {fw && (
        <p className="mt-3 rounded-md border border-buoy px-3 py-1.5 text-sm">
          Firmware {fw.latest_version} is available. Install it from the Deco app.
        </p>
      )}
    </Link>
  );
}

export function Nodes() {
  const { data: nodes = [], isLoading } = useNodes();
  const { data: fw = [] } = useFirmware();
  const { data: net } = useNetwork();
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const master = nodes.filter((n) => n.master);
  const sats = nodes.filter((n) => !n.master);
  return (
    <>
      <PageTitle aside={<p className="text-sm text-drift">{nodes.filter((n) => n.online).length} of {nodes.length} online</p>}>Decos</PageTitle>
      {!isLoading && !nodes.length && <Empty>No Decos found yet. They appear after the first poll.</Empty>}
      <div className="space-y-4">
        {master.map((n) => (
          <NodePanel key={n.id} n={n} fw={upgradeFor(n, fw)} net={net} />
        ))}
        <div className="grid gap-4 md:grid-cols-2">
          {sats.map((n) => (
            <NodePanel key={n.id} n={n} parent={byId.get(n.parent_id)} fw={upgradeFor(n, fw)} />
          ))}
        </div>
      </div>
    </>
  );
}
