import { useState, type ReactNode } from "react";
import { useSaveSettings } from "../api/hooks";
import type { Settings, SettingsPatch } from "../api/types";
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

/** A settings panel with a Save button enabled when something changed. */
function SettingsPanel({
  title,
  changed,
  patch,
  saved,
  children,
  note,
  onSaved,
}: {
  title: string;
  changed: boolean;
  patch: () => SettingsPatch;
  saved: string;
  children: ReactNode;
  note?: ReactNode;
  onSaved?: () => void;
}) {
  const save = useSaveSettings();
  const toast = useToast();
  return (
    <Panel title={title}>
      <div className="space-y-4">{children}</div>
      <ErrorNote error={save.error} />
      <div className="mt-4 flex items-center justify-between gap-4">
        <p className="text-xs text-drift">{note}</p>
        <Button tone="primary" disabled={!changed || save.isPending} onClick={() => save.mutate(patch(), { onSuccess: () => (onSaved?.(), toast(saved)) })}>
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
      changed={changed}
      saved="History settings saved"
      // An untouched value is sent back exactly as it was (hours are rounded for display).
      patch={() => ({
        history: {
          raw_retention: raw === hoursOf(s.history.raw_retention) ? s.history.raw_retention : `${raw}h`,
          rollup_retention: rollup === hoursOf(s.history.rollup_retention) ? s.history.rollup_retention : `${rollup}h`,
        },
      })}
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
      changed={changed}
      saved="Metrics settings saved"
      patch={() => ({ metrics: { enabled, path: path.trim(), per_client: per, token: secret(token.trim(), clear) } })}
      onSaved={() => (setToken(""), setClear(false))}
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
  const [format, setFormat] = useState(s.logging.format);
  const [debug, setDebug] = useState(s.debug.enabled);
  const [trustKey, setTrustKey] = useState(s.security.trust_new_host_key);
  const logChanged = level !== s.logging.level || format !== s.logging.format;
  const changed =
    baseUrl !== s.access.base_url || logChanged || debug !== s.debug.enabled || trustKey !== s.security.trust_new_host_key;
  const facts: [string, string][] = [
    ["Listening on", s.startup.listen],
    ["Database", s.startup.data_path],
    ["Encryption key", s.startup.key_source],
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
        if (logChanged) p.logging = { level, format };
        if (debug !== s.debug.enabled) p.debug = { enabled: debug };
        if (trustKey !== s.security.trust_new_host_key) p.security = { trust_new_host_key: trustKey };
        return p;
      }}
    >
      <Field label="Public address" hint="How you open Atlantis, if through a reverse proxy. With an https:// address, sign-ins over HTTPS get a Secure cookie; the proxy must send X-Forwarded-Proto: https.">
        <Input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://atlantis.example.com" autoComplete="off" />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Log level">
          <select className={`${selectClass} w-full`} value={level} onChange={(e) => setLevel(e.target.value as typeof level)}>
            <option value="error">Errors only</option>
            <option value="warn">Warnings</option>
            <option value="info">Normal</option>
            <option value="debug">Debug (verbose)</option>
          </select>
        </Field>
        <Field label="Log format" hint="Logs go to the console (docker logs).">
          <select className={`${selectClass} w-full`} value={format} onChange={(e) => setFormat(e.target.value as typeof format)}>
            <option value="json">JSON (for log collectors)</option>
            <option value="text">Plain text</option>
          </select>
        </Field>
      </div>
      <div>
        <LabeledSwitch checked={trustKey} onChange={setTrustKey} label="Trust a new Deco SSH key automatically (after a firmware update)" />
        <p className="mt-1 text-xs text-drift">
          {trustKey
            ? "A device on your network pretending to be the Deco would also be trusted. Each change is listed under Events."
            : "When the key changes, Atlantis stops until you accept the new key."}
        </p>
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
          The address and database are chosen when Atlantis starts (--server-listen and --store-path). Back up the database and its key file together.
        </p>
      </div>
    </SettingsPanel>
  );
}
