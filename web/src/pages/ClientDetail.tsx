import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useClient, useEvents, useNodes, useUpdateClient, type Range } from "../api/hooks";
import { ThroughputChart } from "../components/Charts";
import { PresenceStrip } from "../components/Presence";
import { RangeTabs } from "../components/RangeTabs";
import { Bars, Button, ErrorNote, Field, Input, OnlineLabel, Panel, Switch, useToast } from "../components/ui";
import { ago, bandLabel, eventText, networkLabel, rate } from "../lib/format";
import { useNames } from "../lib/names";

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-t border-line py-2 first:border-t-0">
      <dt className="text-drift">{k}</dt>
      <dd className="tnum text-right" dir="auto">
        {children}
      </dd>
    </div>
  );
}

export function ClientDetail() {
  const mac = useParams().mac ?? ""; // already decoded by the router
  const [range, setRange] = useState<Range>("24h");
  const { data, error } = useClient(mac, range);
  const { data: nodes = [] } = useNodes();
  const { data: events = [] } = useEvents();
  const update = useUpdateClient(mac);
  const toast = useToast();
  const name = useNames();
  const c = data?.client;
  const [alias, setAlias] = useState("");
  const [tags, setTags] = useState("");
  useEffect(() => {
    if (c) {
      setAlias(c.alias);
      setTags(c.tags.join(", "));
    }
  }, [c?.alias, c?.tags.join(",")]); // eslint-disable-line react-hooks/exhaustive-deps
  if (error) return <ErrorNote error={error} />;
  if (!c) return <p className="text-drift">Loading…</p>;
  const node = nodes.find((n) => n.id === c.node_id);
  const mine = events.filter((e) => e.subject === c.mac);
  const save = (body: { alias?: string; watch?: boolean; tags?: string[] }, msg: string) =>
    update.mutate(body, { onSuccess: () => toast(msg), onError: (e) => toast((e as Error).message, "bad") });
  return (
    <div className="space-y-6">
      <div>
        <Link to="/clients" className="text-sm text-drift hover:text-abyss">
          Clients
        </Link>
        <div className="mt-1 flex flex-wrap items-center justify-between gap-3">
          <h1 dir="auto" className="text-xl font-semibold tracking-tight">
            {c.display_name}
          </h1>
          <OnlineLabel online={c.online} subject={c.mac} />
        </div>
        {c.alias && c.name && c.alias !== c.name && (
          <p dir="auto" className="text-sm text-drift">
            Reported as {c.name}
          </p>
        )}
      </div>

      <Panel title="Presence" action={<RangeTabs value={range} onChange={setRange} />}>
        <PresenceStrip points={data.history ?? []} events={mine} window={range} height={30} label={c.display_name} showAxis />
      </Panel>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,7fr)_minmax(0,5fr)]">
        <Panel title="Throughput">
          <ThroughputChart points={data.history ?? []} long={range !== "24h"} />
          {c.online && (
            <p className="tnum mt-2 text-sm text-drift">
              Now: {rate(c.down_bps)} down, {rate(c.up_bps)} up
            </p>
          )}
        </Panel>
        <Panel title="Details">
          <dl className="text-sm">
            <Row k="Connected to">
              {node ? (
                <Link to={`/nodes/${encodeURIComponent(node.id)}`} className="hover:underline">
                  {node.nickname || node.model}
                </Link>
              ) : (
                "—"
              )}
            </Row>
            <Row k="Connection">
              <span className="inline-flex items-center gap-2">
                {bandLabel(c.band, c.wired)} {!c.wired && c.online && c.signal_level > 0 && <Bars level={c.signal_level} />}
              </span>
            </Row>
            <Row k="Network">{networkLabel(c.network)}</Row>
            <Row k="IP address">{c.ip || "—"}</Row>
            <Row k="MAC address">
              {c.mac}
              {c.private_mac && <span className="block text-xs text-drift">Private (randomised) address</span>}
            </Row>
            {c.type && <Row k="Type">{c.type.replace("_", " ")}</Row>}
            <Row k="First seen">{new Date(c.first_seen).toLocaleDateString()}</Row>
            <Row k="Last seen">{c.online ? "now" : ago(c.last_seen)}</Row>
          </dl>
        </Panel>
      </div>

      <Panel title="In Atlantis">
        <div className="grid gap-4 md:grid-cols-2">
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              save({ alias: alias.trim(), tags: tags.split(",").map((t) => t.trim()).filter(Boolean) }, "Saved");
            }}
          >
            <Field label="Name in Atlantis" hint="Only Atlantis uses this name; the Deco keeps its own.">
              <Input value={alias} onChange={(e) => setAlias(e.target.value)} placeholder={c.name || c.mac} maxLength={64} dir="auto" />
            </Field>
            <Field label="Tags" hint="Separate tags with commas.">
              <Input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="kids, work" dir="auto" />
            </Field>
            <Button tone="primary" type="submit" disabled={update.isPending}>
              Save
            </Button>
          </form>
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <div>
                <p className="font-medium">Watch this device</p>
                <p className="text-sm text-drift">Watched devices can notify you when they come and go, and get their own metrics.</p>
              </div>
              <Switch
                checked={c.watch}
                label="Watch this device"
                onChange={(v) => save({ watch: v }, v ? "Now watching" : "No longer watching")}
              />
            </div>
          </div>
        </div>
      </Panel>

      {mine.length > 0 && (
        <Panel title="Recent changes">
          <ul className="space-y-1.5 text-sm">
            {mine.slice(0, 12).map((e) => (
              <li key={e.id} className="flex gap-3">
                <time className="tnum w-36 shrink-0 text-drift">{new Date(e.ts).toLocaleString([], { dateStyle: "short", timeStyle: "short" })}</time>
                <span dir="auto">{eventText(e, name)}</span>
              </li>
            ))}
          </ul>
        </Panel>
      )}
    </div>
  );
}
