import { useState, type ReactNode } from "react";
import { useSaveSettings } from "../api/hooks";
import type { Settings, SettingSource, SettingsPatch } from "../api/types";
import { goDuration } from "../lib/format";
import { Button, ErrorNote, Field, Input, Panel, Switch, selectClass, useToast } from "./ui";

/** Go duration ("168h0m0s", "2h30m") to whole hours. */
export function hoursOf(d: string): number {
  const h = /(\d+)h/.exec(d)?.[1] ?? "0";
  const m = /(\d+)m(?!s)/.exec(d)?.[1] ?? "0";
  return Number(h) + Math.round(Number(m) / 60);
}

/** A switch with its label shown beside it. */
function LabeledSwitch({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <div className="flex items-center gap-3">
      <Switch checked={checked} onChange={onChange} label={label} />
      <span className="text-sm">{label}</span>
    </div>
  );
}

const sourceNote = (src: SettingSource) => (src === "ui" ? "Set here." : "From the configuration file until you save here.");

/** A settings panel: a Save button enabled when something changed, and where the values come from. */
function SettingsPanel({
  title,
  source,
  changed,
  patch,
  saved,
  children,
  note,
}: {
  title: string;
  source?: SettingSource;
  changed: boolean;
  patch: () => SettingsPatch;
  saved: string;
  children: ReactNode;
  note?: ReactNode;
}) {
  const save = useSaveSettings();
  const toast = useToast();
  return (
    <Panel title={title}>
      <div className="space-y-4">{children}</div>
      <ErrorNote error={save.error} />
      <div className="mt-4 flex items-center justify-between gap-4">
        <p className="text-xs text-drift">
          {source && sourceNote(source)} {note}
        </p>
        <Button tone="primary" disabled={!changed || save.isPending} onClick={() => save.mutate(patch(), { onSuccess: () => toast(saved) })}>
          Save
        </Button>
      </div>
    </Panel>
  );
}

const withCurrent = (choices: number[], cur: number) => [...new Set([...choices, cur])].sort((a, b) => a - b);
const hoursLabel = (h: number) => goDuration(`${h}h0m0s`);

export function HistoryPanel({ s }: { s: Settings }) {
  const [raw, setRaw] = useState(hoursOf(s.history.raw_retention));
  const [rollup, setRollup] = useState(hoursOf(s.history.rollup_retention));
  const changed = raw !== hoursOf(s.history.raw_retention) || rollup !== hoursOf(s.history.rollup_retention);
  return (
    <SettingsPanel
      title="History"
      source={s.history.source}
      changed={changed}
      saved="History settings saved"
      patch={() => ({ history: { raw_retention: `${raw}h`, rollup_retention: `${rollup}h` } })}
      note="Older data is removed within the hour."
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Keep detailed samples for" hint="Every poll; used for the 24-hour views.">
          <select className={`${selectClass} w-full`} value={raw} onChange={(e) => setRaw(Number(e.target.value))}>
            {withCurrent([24, 72, 168, 336, 720], raw).map((h) => (
              <option key={h} value={h}>
                {hoursLabel(h)}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Keep hourly history for" hint="Used for the 7- and 30-day views. At least as long as detailed samples.">
          <select className={`${selectClass} w-full`} value={rollup} onChange={(e) => setRollup(Number(e.target.value))}>
            {withCurrent([720, 2160, 4320, 8760, 17520], rollup).map((h) => (
              <option key={h} value={h} disabled={h < raw}>
                {hoursLabel(h)}
              </option>
            ))}
          </select>
        </Field>
      </div>
    </SettingsPanel>
  );
}

function timeZones(current: string): string[] {
  let all: string[];
  try {
    all = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  } catch {
    all = [];
  }
  return [...new Set(["UTC", current, ...all])].filter(Boolean);
}

export function NotificationsPanel({ s }: { s: Settings }) {
  const [from0, to0] = (s.notifications.quiet_hours || "23:00-07:00").split("-");
  const [quiet, setQuiet] = useState(!!s.notifications.quiet_hours);
  const [from, setFrom] = useState(from0);
  const [to, setTo] = useState(to0);
  const [tz, setTz] = useState(s.notifications.timezone);
  const hours = quiet ? `${from}-${to}` : "";
  const changed = hours !== s.notifications.quiet_hours || tz !== s.notifications.timezone;
  return (
    <SettingsPanel
      title="Notification timing"
      source={s.notifications.source}
      changed={changed}
      saved="Notification timing saved"
      patch={() => ({ notifications: { quiet_hours: hours, timezone: tz } })}
    >
      <Field label="Time zone" hint="Used for quiet hours.">
        <select className={`${selectClass} w-full`} value={tz} onChange={(e) => setTz(e.target.value)}>
          {timeZones(s.notifications.timezone).map((z) => (
            <option key={z} value={z}>
              {z.replaceAll("_", " ")}
            </option>
          ))}
        </select>
      </Field>
      <LabeledSwitch checked={quiet} onChange={setQuiet} label="Quiet hours: send nothing during these hours" />
      {quiet && (
        <div className="grid grid-cols-2 gap-4 sm:max-w-sm">
          <Field label="From">
            <Input type="time" value={from} onChange={(e) => setFrom(e.target.value)} required />
          </Field>
          <Field label="Until">
            <Input type="time" value={to} onChange={(e) => setTo(e.target.value)} required />
          </Field>
        </div>
      )}
    </SettingsPanel>
  );
}

/** A write-only secret: shows whether one is saved; type to replace, or clear it. */
function SecretField({ label, hint, isSet, value, onChange, cleared, onClear }: {
  label: string;
  hint: string;
  isSet: boolean;
  value: string;
  onChange: (v: string) => void;
  cleared: boolean;
  onClear: (v: boolean) => void;
}) {
  return (
    <Field label={label} hint={isSet && !cleared ? `${hint} A value is saved; type a new one to replace it.` : hint}>
      <div className="flex items-center gap-3">
        <Input
          type="password"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={isSet && !cleared ? "•••••••• (saved)" : ""}
          autoComplete="off"
          disabled={cleared}
        />
        {isSet && (
          <label className="flex shrink-0 items-center gap-1.5 text-sm">
            <input type="checkbox" checked={cleared} onChange={(e) => onClear(e.target.checked)} className="accent-flare" />
            Remove
          </label>
        )}
      </div>
    </Field>
  );
}

const secret = (value: string, cleared: boolean) => (cleared ? "" : value ? value : undefined);

export function OpenWAPanel({ s }: { s: Settings }) {
  const [url, setUrl] = useState(s.openwa.url);
  const [chat, setChat] = useState(s.openwa.default_chat);
  const [key, setKey] = useState("");
  const [clear, setClear] = useState(false);
  const changed = url !== s.openwa.url || chat !== s.openwa.default_chat || key !== "" || clear;
  return (
    <SettingsPanel
      title="WhatsApp (OpenWA) defaults"
      source={s.openwa.source}
      changed={changed}
      saved="WhatsApp defaults saved"
      patch={() => ({ openwa: { url: url.trim(), default_chat: chat.trim(), api_key: secret(key.trim(), clear) } })}
      note="WhatsApp channels that leave these empty use them."
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="OpenWA server URL">
          <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://openwa.lan:8002" autoComplete="off" />
        </Field>
        <Field label="Default chat" hint="A contact or group ID.">
          <Input value={chat} onChange={(e) => setChat(e.target.value)} autoComplete="off" />
        </Field>
      </div>
      <SecretField label="API key" hint="Stored encrypted and never shown." isSet={s.openwa.api_key_set} value={key} onChange={setKey} cleared={clear} onClear={setClear} />
    </SettingsPanel>
  );
}

export function MetricsPanel({ s }: { s: Settings }) {
  const [enabled, setEnabled] = useState(s.metrics.enabled);
  const [path, setPath] = useState(s.metrics.path);
  const [per, setPer] = useState(s.metrics.per_client);
  const [token, setToken] = useState("");
  const [clear, setClear] = useState(false);
  const changed = enabled !== s.metrics.enabled || path !== s.metrics.path || per !== s.metrics.per_client || token !== "" || clear;
  return (
    <SettingsPanel
      title="Prometheus metrics"
      source={s.metrics.source}
      changed={changed}
      saved="Metrics settings saved"
      patch={() => ({ metrics: { enabled, path: path.trim(), per_client: per, token: secret(token.trim(), clear) } })}
      note={enabled ? `Scrape ${location.origin}${path}.` : undefined}
    >
      <LabeledSwitch checked={enabled} onChange={setEnabled} label="Serve Prometheus metrics" />
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Path">
          <Input value={path} onChange={(e) => setPath(e.target.value)} placeholder="/metrics" disabled={!enabled} />
        </Field>
        <Field label="Per-device series" hint="Device names and MACs become metric labels.">
          <select className={`${selectClass} w-full`} value={per} onChange={(e) => setPer(e.target.value as typeof per)} disabled={!enabled}>
            <option value="watched">Watched devices</option>
            <option value="all">All devices</option>
            <option value="none">None</option>
          </select>
        </Field>
      </div>
      <SecretField
        label="Bearer token for Prometheus"
        hint="Without one, metrics need a signed-in browser. Stored encrypted and never shown."
        isSet={s.metrics.token_set}
        value={token}
        onChange={setToken}
        cleared={clear}
        onClear={setClear}
      />
    </SettingsPanel>
  );
}

export function AdvancedPanel({ s }: { s: Settings }) {
  const [baseUrl, setBaseUrl] = useState(s.access.base_url);
  const [level, setLevel] = useState(s.logging.level);
  const [debug, setDebug] = useState(s.debug.enabled);
  const changed = baseUrl !== s.access.base_url || level !== s.logging.level || debug !== s.debug.enabled;
  const facts: [string, string][] = [
    ["Listening on", s.startup.listen],
    ["Database", s.startup.data_path],
    ["Encryption key", s.startup.key_source],
    ["Log format", s.startup.log_format],
    ["Version", s.version],
  ];
  return (
    <SettingsPanel
      title="Advanced"
      changed={changed}
      saved="Advanced settings saved"
      patch={() => {
        const p: SettingsPatch = {};
        if (baseUrl !== s.access.base_url) p.access = { base_url: baseUrl.trim() };
        if (level !== s.logging.level) p.logging = { level };
        if (debug !== s.debug.enabled) p.debug = { enabled: debug };
        return p;
      }}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Public address" hint="How you open Atlantis, if through a reverse proxy. An https:// address keeps the sign-in cookie on HTTPS only.">
          <Input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://atlantis.example.com" autoComplete="off" />
        </Field>
        <Field label="Log level">
          <select className={`${selectClass} w-full`} value={level} onChange={(e) => setLevel(e.target.value as typeof level)}>
            <option value="error">Errors only</option>
            <option value="warn">Warnings</option>
            <option value="info">Normal</option>
            <option value="debug">Debug (verbose)</option>
          </select>
        </Field>
      </div>
      <div>
        <LabeledSwitch checked={debug} onChange={setDebug} label="Debug endpoint (raw Deco replies, redacted)" />
        {debug && <p className="mt-1 text-xs text-buoy">Turn this off again when you are done; it exposes raw replies to signed-in users.</p>}
      </div>
      <div>
        <p className="text-sm font-medium">Set when Atlantis starts</p>
        <dl className="mt-1 text-sm">
          {facts.map(([k, v]) => (
            <div key={k} className="flex justify-between gap-4 border-t border-line py-2 first:border-t-0">
              <dt className="text-drift">{k}</dt>
              <dd className="tnum text-right break-all">{v}</dd>
            </div>
          ))}
        </dl>
        <p className="mt-1 text-xs text-drift">
          To change these, start Atlantis with ATLANTIS_SERVER_LISTEN, ATLANTIS_STORE_PATH, ATLANTIS_SECRET_KEY or ATLANTIS_LOG_FORMAT. Back up the database and its key
          together.
        </p>
      </div>
    </SettingsPanel>
  );
}
