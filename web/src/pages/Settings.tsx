import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import {
  useChannelMutations,
  useChannels,
  useDeco,
  useDecoMutations,
  useSession,
  useSessionActions,
  useSaveSettings,
  useSettings,
  type ChannelInput,
} from "../api/hooks";
import type { Channel, EventType, Settings as SettingsData } from "../api/types";
import { DecoForm } from "../components/DecoForm";
import { AdvancedPanel, HistoryPanel, MetricsPanel, NotificationsPanel } from "../components/SettingsPanels";
import {
  Button,
  Confirm,
  Dialog,
  Empty,
  ErrorNote,
  Field,
  Input,
  PageTitle,
  Panel,
  Switch,
  inputClass,
  selectClass,
  sessionDot,
  useToast,
} from "../components/ui";
import { ago, goDuration, sessionSentence } from "../lib/format";

const eventNames: Record<EventType, string> = {
  "client.joined": "A new device joins for the first time",
  "client.online": "A device comes online",
  "client.offline": "A device goes offline",
  "node.online": "A Deco comes back online",
  "node.offline": "A Deco goes offline",
  "wan.up": "Internet comes back",
  "wan.down": "Internet goes down",
  "wan.ip_changed": "The public IP changes",
  "firmware.changed": "A Deco's firmware changes",
  "deco.host_key_changed": "The Deco's SSH key changes",
};

function SessionPanel() {
  const { data: s } = useSession();
  const a = useSessionActions();
  const toast = useToast();
  const { text, tone } = sessionSentence(s);
  const cb = (msg: string) => ({ onSuccess: () => toast(msg), onError: (e: Error) => toast(e.message, "bad") });
  return (
    <Panel title="Connection to the Deco">
      <p className="flex items-center gap-2 text-sm">
        {sessionDot(tone)} {text}
      </p>
      {s && (
        <p className="tnum mt-1 text-xs text-drift">
          Last successful read {ago(s.last_ok)} · {s.logins} sign-ins since start
        </p>
      )}
      <p className="mt-3 text-sm text-drift">
        The Deco lets only one admin in at a time. Release the session to use the Deco app; Atlantis signs back in when the time is up.
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        <Button onClick={() => a.release.mutate(10, cb("Released for 10 minutes"))}>Release for 10 minutes</Button>
        <Button onClick={() => a.release.mutate(60, cb("Released for an hour"))}>Release for an hour</Button>
        {s?.state === "paused" ? (
          <Button tone="primary" onClick={() => a.resume.mutate(undefined, cb("Resumed"))}>
            Resume
          </Button>
        ) : (
          <Button onClick={() => a.pause.mutate(undefined, cb("Polling paused"))}>Pause polling</Button>
        )}
      </div>
    </Panel>
  );
}

const blank = (): ChannelInput => ({ kind: "shoutrrr", name: "", enabled: true, events: ["client.joined", "node.offline", "wan.down", "wan.up"], watched_only: false, template: "", config: { url: "" } });

function ChannelDialog({ open, onOpenChange, editing, allEvents }: { open: boolean; onOpenChange: (v: boolean) => void; editing?: Channel; allEvents: EventType[] }) {
  const m = useChannelMutations();
  const toast = useToast();
  const [c, setC] = useState<ChannelInput>(() =>
    editing ? { kind: editing.kind, name: editing.name, enabled: editing.enabled, events: editing.events, watched_only: editing.watched_only, template: editing.template } : blank(),
  );
  const [error, setError] = useState<unknown>(null);
  // Secrets are write-only: when editing, the config is only sent if retyped.
  const [cfg, setCfg] = useState({ url: "", api_key: "", chat: "" });
  const cfgTyped = cfg.url.trim() !== "" || cfg.api_key.trim() !== "" || cfg.chat.trim() !== "";
  const body = (): ChannelInput => ({
    ...c,
    name: c.name.trim(),
    config: cfgTyped || !editing ? (c.kind === "openwa" ? { url: cfg.url.trim(), api_key: cfg.api_key.trim(), chat: cfg.chat.trim() } : { url: cfg.url.trim() }) : undefined,
  });
  const toggleEvent = (e: string) => setC({ ...c, events: c.events.includes(e) ? c.events.filter((x) => x !== e) : [...c.events, e] });
  const save = () => {
    setError(null);
    const done = { onSuccess: () => (toast(editing ? "Channel saved" : "Channel added"), onOpenChange(false)), onError: setError };
    if (editing) m.update.mutate({ id: editing.id, ...body() }, done);
    else m.create.mutate(body(), done);
  };
  const test = () => {
    setError(null);
    const done = { onSuccess: () => toast("Test message sent"), onError: setError };
    if (editing && !cfgTyped) m.testSaved.mutate(editing.id, done);
    else m.testDraft.mutate(body(), done);
  };
  const keep = editing?.configured ? "Leave blank to keep the saved value" : undefined;
  // The server replaces the whole config, so once any OpenWA field is
  // retyped the address and chat are needed again (and the key, if used).
  const needAll = !editing || cfgTyped;
  const keepAll = editing?.configured && !cfgTyped ? "Leave all three blank to keep the saved values" : undefined;
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={editing ? `Edit ${editing.name}` : "Add a notification channel"}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <Field label="Service">
          <select className={inputClass} value={c.kind} onChange={(e) => setC({ ...c, kind: e.target.value as Channel["kind"] })} disabled={!!editing}>
            <option value="shoutrrr">Shoutrrr (Telegram, Discord, Slack, ntfy, email and more)</option>
            <option value="openwa">WhatsApp (OpenWA)</option>
          </select>
        </Field>
        <Field label="Name">
          <Input value={c.name} onChange={(e) => setC({ ...c, name: e.target.value })} placeholder="Family Telegram" required dir="auto" />
        </Field>
        {c.kind === "shoutrrr" ? (
          <Field label="Shoutrrr URL" hint={keep ?? "For example telegram://token@telegram?chats=@channel"}>
            <Input type="password" value={cfg.url} onChange={(e) => setCfg({ ...cfg, url: e.target.value })} required={!editing} autoComplete="off" />
          </Field>
        ) : (
          <>
            <Field label="OpenWA address" hint={keepAll ?? "For example http://openwa:8002"}>
              <Input value={cfg.url} onChange={(e) => setCfg({ ...cfg, url: e.target.value })} required={needAll} autoComplete="off" />
            </Field>
            <Field label="API key" hint="Only if your OpenWA server uses one.">
              <Input type="password" value={cfg.api_key} onChange={(e) => setCfg({ ...cfg, api_key: e.target.value })} autoComplete="off" />
            </Field>
            <Field label="Send to" hint="A phone number in international format with no + (972501234567), or a group ID">
              <Input value={cfg.chat} onChange={(e) => setCfg({ ...cfg, chat: e.target.value })} required={needAll} autoComplete="off" />
            </Field>
          </>
        )}
        <fieldset>
          <legend className="text-sm font-medium">Notify me when</legend>
          <div className="mt-1 space-y-1">
            {allEvents.map((e) => (
              <label key={e} className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={c.events.includes(e)} onChange={() => toggleEvent(e)} /> {eventNames[e] ?? e}
              </label>
            ))}
          </div>
        </fieldset>
        <label className="flex items-center justify-between gap-3 text-sm">
          <span>Only for watched devices</span>
          <Switch checked={c.watched_only} onChange={(v) => setC({ ...c, watched_only: v })} label="Only for watched devices" />
        </label>
        <label className="flex items-center justify-between gap-3 text-sm">
          <span>Enabled</span>
          <Switch checked={c.enabled ?? true} onChange={(v) => setC({ ...c, enabled: v })} label="Enabled" />
        </label>
        <Field label="Message template" hint="Optional. Go template; leave blank for the default message, for example {{.Text}}.">
          <textarea className={inputClass + " min-h-16"} value={c.template} onChange={(e) => setC({ ...c, template: e.target.value })} dir="auto" />
        </Field>
        <ErrorNote error={error} />
        <div className="flex justify-between gap-2">
          <Button type="button" onClick={test} disabled={m.testDraft.isPending || m.testSaved.isPending}>
            {m.testDraft.isPending || m.testSaved.isPending ? "Sending…" : "Send test"}
          </Button>
          <div className="flex gap-2">
            <Button type="button" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button tone="primary" type="submit" disabled={m.create.isPending || m.update.isPending}>
              {editing ? "Save" : "Add channel"}
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}

function ChannelsPanel({ allEvents }: { allEvents: EventType[] }) {
  const { data: channels = [], error } = useChannels();
  const m = useChannelMutations();
  const toast = useToast();
  const [dialog, setDialog] = useState<{ open: boolean; editing?: Channel; key: number }>({ open: false, key: 0 });
  const [removing, setRemoving] = useState<Channel | null>(null);
  const open = (editing?: Channel) => setDialog({ open: true, editing, key: dialog.key + 1 });
  return (
    <Panel
      title="Notifications"
      action={
        <Button onClick={() => open()}>
          <Plus size={15} aria-hidden /> Add channel
        </Button>
      }
    >
      <ErrorNote error={error} />
      {channels.length ? (
        <ul className="divide-y divide-line">
          {channels.map((c) => (
            <li key={c.id} className="flex items-center gap-3 py-2.5">
              <Switch
                checked={c.enabled}
                label={`${c.name} enabled`}
                onChange={(v) =>
                  m.update.mutate(
                    { id: c.id, kind: c.kind, name: c.name, events: c.events, watched_only: c.watched_only, template: c.template, enabled: v },
                    { onError: (e) => toast((e as Error).message, "bad") },
                  )
                }
              />
              <span className="min-w-0 flex-1">
                <span dir="auto" className="block truncate font-medium">
                  {c.name}
                </span>
                <span className="block text-xs text-drift">
                  {c.kind === "openwa" ? "WhatsApp" : "Shoutrrr"} · {c.events.length ? `${c.events.length} kinds of change` : "every change"}
                  {c.watched_only && " · watched devices only"}
                </span>
              </span>
              <Button
                onClick={() =>
                  m.testSaved.mutate(c.id, { onSuccess: () => toast(`Test sent to ${c.name}`), onError: (e) => toast((e as Error).message, "bad") })
                }
              >
                Send test
              </Button>
              <button aria-label={`Edit ${c.name}`} onClick={() => open(c)} className="rounded p-1.5 text-drift hover:text-abyss">
                <Pencil size={16} />
              </button>
              <button aria-label={`Delete ${c.name}`} onClick={() => setRemoving(c)} className="rounded p-1.5 text-drift hover:text-flare">
                <Trash2 size={16} />
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <Empty>No channels yet. Add one to hear about new devices, offline Decos and internet outages.</Empty>
      )}
      {dialog.open && <ChannelDialog key={dialog.key} open onOpenChange={(v) => setDialog({ ...dialog, open: v })} editing={dialog.editing} allEvents={allEvents} />}
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={`Delete ${removing?.name ?? ""}?`}
        body={<p>Atlantis stops sending to this channel. Its saved credentials are deleted.</p>}
        action="Delete channel"
        busy={m.remove.isPending}
        onConfirm={() =>
          removing &&
          m.remove.mutate(removing.id, {
            onSuccess: () => (setRemoving(null), toast("Channel deleted")),
            onError: (e) => toast((e as Error).message, "bad"),
          })
        }
      />
    </Panel>
  );
}

function DecoPanel() {
  const { data: d } = useDeco();
  const m = useDecoMutations();
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  if (!d?.configured) return null;
  const rows: [string, string][] = [
    ["Address", `${d.host}${d.port ? `:${d.port}` : ""}`],
    ["Model", d.discovery?.model ? `Deco ${d.discovery.model}` : "—"],
    ["Connection", d.transport === "luci" ? "Local web page" : "Deco app protocol (SSH)"],
    ["TP-Link account", d.email || "—"],
    ["Security key", d.host_key || "—"],
  ];
  return (
    <Panel
      title="Deco"
      action={
        <div className="flex gap-2">
          <Button onClick={() => (m.change.reset(), setEditing(true))}>Change connection</Button>
          <Button tone="danger" onClick={() => setRemoving(true)}>
            Remove
          </Button>
        </div>
      }
    >
      <dl className="text-sm">
        {rows.map(([k, v]) => (
          <div key={k} className="flex justify-between gap-4 border-t border-line py-2 first:border-t-0">
            <dt className="text-drift">{k}</dt>
            <dd className="tnum text-right break-all">{v}</dd>
          </div>
        ))}
      </dl>
      <Dialog open={editing} onOpenChange={setEditing} title="Change the Deco connection">
        <DecoForm
          current={d}
          busy={m.change.isPending}
          error={m.change.error}
          submitLabel="Save and reconnect"
          onCancel={() => setEditing(false)}
          onSubmit={(c) => m.change.mutate(c, { onSuccess: () => (setEditing(false), toast("Reconnected to the Deco")) })}
        />
      </Dialog>
      <Confirm
        open={removing}
        onOpenChange={setRemoving}
        title="Remove the Deco?"
        body={
          <p>
            Atlantis signs out, forgets this Deco and deletes its devices, history and events. Notification channels and your account stay. You can
            connect a Deco again afterwards.
          </p>
        }
        action="Remove"
        typed="REMOVE"
        busy={m.remove.isPending}
        onConfirm={() =>
          m.remove.mutate(undefined, {
            onSuccess: () => (setRemoving(false), toast("Deco removed")),
            onError: (e) => toast((e as Error).message, "bad"),
          })
        }
      />
    </Panel>
  );
}

const fastChoices = ["10s", "15s", "30s", "1m", "2m", "5m"];
const slowChoices = ["1m", "2m", "5m", "10m", "15m", "30m", "1h"];
// Go prints durations as "1m0s"; the choices are written short.
const short = (d: string) => d.replace(/(\d)m0s$/, "$1m").replace(/(\d)h0m$/, "$1h").replace(/^1h0m0s$/, "1h");
const withCurrent = (choices: string[], cur: string) => (choices.includes(cur) ? choices : [cur, ...choices]);

function RefreshPanel({ s }: { s: SettingsData }) {
  const set = useSaveSettings();
  const toast = useToast();
  const [fast, setFast] = useState(short(s.poll.fast));
  const [slow, setSlow] = useState(short(s.poll.slow));
  const [misses, setMisses] = useState(s.poll.offline_after_misses);
  const changed = fast !== short(s.poll.fast) || slow !== short(s.poll.slow) || misses !== s.poll.offline_after_misses;
  return (
    <Panel title="Refresh">
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Check devices every" hint="Who is online, speeds and load.">
          <select className={`${selectClass} w-full`} value={fast} onChange={(e) => setFast(e.target.value)}>
            {withCurrent(fastChoices, fast).map((v) => (
              <option key={v} value={v}>
                {goDuration(v)}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Check the mesh every" hint="Decos, internet, Wi-Fi and firmware.">
          <select className={`${selectClass} w-full`} value={slow} onChange={(e) => setSlow(e.target.value)}>
            {withCurrent(slowChoices, slow).map((v) => (
              <option key={v} value={v}>
                {goDuration(v)}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Mark a device offline after" hint="Missed device checks in a row.">
          <select className={`${selectClass} w-full`} value={misses} onChange={(e) => setMisses(Number(e.target.value))}>
            {[...new Set([1, 2, 3, 4, 5, misses])].sort((a, b) => a - b).map((n) => (
              <option key={n} value={n}>
                {n} missed {n === 1 ? "check" : "checks"}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <ErrorNote error={set.error} />
      <div className="mt-4 flex items-center justify-between gap-4">
        <p className="text-xs text-drift">
          Faster checks mean more sign-in traffic to the Deco.
        </p>
        <Button
          tone="primary"
          disabled={!changed || set.isPending}
          onClick={() => set.mutate({ poll: { fast, slow, offline_after_misses: misses } }, { onSuccess: () => toast("Refresh times saved") })}
        >
          Save
        </Button>
      </div>
    </Panel>
  );
}

export function Settings() {
  const { data: s, error } = useSettings();
  // Panels remount when the saved values change, so their fields follow.
  const k = (o: object) => JSON.stringify(o);
  return (
    <>
      <PageTitle>Settings</PageTitle>
      <div className="space-y-6">
        <ErrorNote error={error} />
        <DecoPanel />
        <SessionPanel />
        {s && <RefreshPanel key={`${s.poll.fast}-${s.poll.slow}-${s.poll.offline_after_misses}`} s={s} />}
        <ChannelsPanel allEvents={s?.notifications.events ?? []} />
        {s && (
          <>
            <NotificationsPanel key={k(s.notifications)} s={s} />
            <HistoryPanel key={k(s.history)} s={s} />
            <MetricsPanel key={k(s.metrics)} s={s} />
            <AdvancedPanel key={k([s.access, s.logging, s.debug])} s={s} />
          </>
        )}
      </div>
    </>
  );
}
