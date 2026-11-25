import { Pencil } from "lucide-react";
import { useState } from "react";
import { useSetWifi, useWifi } from "../api/hooks";
import type { Wireless, WirelessPatch } from "../api/types";
import { Button, Confirm, Dialog, Empty, ErrorNote, Field, Input, PageTitle, Panel, Switch, selectClass, useToast } from "../components/ui";
import { bandLabel, networkLabel } from "../lib/format";

const bandOrder = ["band2_4", "band5", "band5_1", "band5_2", "band6", "band6_2", "band60", "mlo"];

const securityNames: Record<string, string> = {
  none: "Open (no password)",
  psk: "WPA2",
  wpa2: "WPA2",
  "wpa2+wpa": "WPA/WPA2",
  wpa3: "WPA3",
  "wpa2+wpa3": "WPA2/WPA3",
};
const securityLabel = (m: string) => securityNames[m] ?? m ?? "—";
const securityChoices = ["wpa2", "wpa2+wpa", "none"];

const what: Record<string, string> = {
  host: "Your devices' everyday network.",
  guest: "A separate network for visitors, kept apart from your devices.",
  iot: "A separate network for smart-home devices.",
  mlo: "Wi-Fi 7 multi-link network.",
};

/** One network's state across bands. */
function summary(data: Wireless, nw: string, bands: string[]) {
  const on = bands.filter((b) => data.bands[b]?.networks?.[nw]?.enable);
  const any = bands.map((b) => data.bands[b]?.networks?.[nw]).find(Boolean);
  return { on, any, present: bands.filter((b) => data.bands[b]?.networks?.[nw]) };
}

function NetworkPanel({ data, nw, bands }: { data: Wireless; nw: string; bands: string[] }) {
  const set = useSetWifi();
  const toast = useToast();
  const editable = data.editable?.[nw] ?? [];
  const { on, any, present } = summary(data, nw, bands);
  const [toggle, setToggle] = useState<boolean | null>(null);
  const [editing, setEditing] = useState(false);
  const [pending, setPending] = useState<WirelessPatch | null>(null);
  if (!any) return null;
  const label = networkLabel(nw);
  const isOn = on.length > 0;
  const canToggle = editable.includes("enable");
  const canEdit = ["ssid", "password", "encryption", "hidden"].some((f) => editable.includes(f));
  const send = (p: WirelessPatch, done: string) =>
    set.mutate(p, {
      onSuccess: () => (setToggle(null), setPending(null), setEditing(false), toast(done)),
      onError: (e) => toast((e as Error).message, "bad"),
    });
  return (
    <Panel
      title={`${label} network`}
      action={
        <div className="flex items-center gap-3">
          {canToggle && <Switch checked={isOn} onChange={(v) => setToggle(v)} label={`${label} network ${isOn ? "on" : "off"}`} />}
          {canEdit && (
            <Button onClick={() => (set.reset(), setEditing(true))}>
              <Pencil size={14} aria-hidden /> Edit
            </Button>
          )}
        </div>
      }
    >
      <p className="text-sm text-drift">{what[nw] ?? ""}</p>
      <dl className="mt-3 grid gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
        <div>
          <dt className="text-xs text-drift">Name</dt>
          <dd dir="auto" className="break-all">
            {any.ssid || "—"}
            {any.hidden && <span className="text-xs text-drift"> (hidden)</span>}
          </dd>
        </div>
        <div>
          <dt className="text-xs text-drift">Security</dt>
          <dd>{securityLabel(any.encryption)}</dd>
        </div>
        <div>
          <dt className="text-xs text-drift">On</dt>
          <dd>{isOn ? on.map((b) => bandLabel(b)).join(", ") : "Off"}</dd>
        </div>
      </dl>
      {!canToggle && !canEdit && <p className="mt-3 text-xs text-drift">Atlantis can't change this network yet. Use the Deco app.</p>}

      <Confirm
        open={toggle !== null}
        onOpenChange={(v) => !v && setToggle(null)}
        title={toggle ? `Turn on the ${label.toLowerCase()} network?` : `Turn off the ${label.toLowerCase()} network?`}
        body={
          toggle ? (
            <p>
              <strong dir="auto">{any.ssid}</strong> appears on {present.map((b) => bandLabel(b)).join(", ")}
              {any.encryption === "none" ? " as an open network anyone nearby can join." : "."}
            </p>
          ) : (
            <p>Every device on {label.toLowerCase()} Wi-Fi disconnects. Your other networks are not affected.</p>
          )
        }
        action={toggle ? "Turn on" : "Turn off"}
        busy={set.isPending}
        onConfirm={() => send({ network: nw, enable: !!toggle }, toggle ? `${label} network turned on` : `${label} network turned off`)}
      />
      <Dialog open={editing} onOpenChange={setEditing} title={`Change the ${label.toLowerCase()} network`}>
        <EditForm
          nw={nw}
          current={any}
          editable={editable}
          busy={set.isPending}
          error={set.error}
          onCancel={() => setEditing(false)}
          onSubmit={(p) => (nw === "host" ? setPending(p) : send(p, `${label} network changed`))}
        />
      </Dialog>
      <Confirm
        open={pending !== null}
        onOpenChange={(v) => !v && setPending(null)}
        title="Change the main Wi-Fi?"
        body={
          <>
            <p>
              Every device on the main network disconnects. Each one must join <strong dir="auto">{pending?.ssid ?? any.ssid}</strong>
              {pending?.password ? " with the new password" : ""}.
            </p>
            <p>If you're reading this over Wi-Fi, you'll lose this page too.</p>
          </>
        }
        typed="CHANGE"
        action="Change Wi-Fi"
        busy={set.isPending}
        onConfirm={() => pending && send(pending, `Wi-Fi changed to ${pending.ssid ?? any.ssid}`)}
      />
    </Panel>
  );
}

function EditForm({
  nw,
  current,
  editable,
  busy,
  error,
  onSubmit,
  onCancel,
}: {
  nw: string;
  current: { ssid: string; encryption: string; hidden: boolean };
  editable: string[];
  busy: boolean;
  error: unknown;
  onSubmit: (p: WirelessPatch) => void;
  onCancel: () => void;
}) {
  const [ssid, setSsid] = useState(current.ssid);
  const [password, setPassword] = useState("");
  const [security, setSecurity] = useState(current.encryption);
  const [hidden, setHidden] = useState(current.hidden);
  const can = (f: string) => editable.includes(f);
  const open = security === "none";
  const securing = current.encryption === "none" && !open;
  const ssidBytes = new TextEncoder().encode(ssid.trim()).length;
  const needsPassword = securing || (nw === "host" && can("password") && !can("ssid"));
  const pwOk = password === "" ? !needsPassword : password.length >= 8 && password.length <= 63;
  const patch: WirelessPatch = { network: nw };
  if (can("ssid") && ssid.trim() !== current.ssid) patch.ssid = ssid.trim();
  if (can("password") && password && !open) patch.password = password;
  if (can("encryption") && security !== current.encryption) patch.encryption = security;
  if (can("hidden") && hidden !== current.hidden) patch.hidden = hidden;
  const changed = Object.keys(patch).length > 1;
  const valid = changed && ssidBytes > 0 && ssidBytes <= 32 && pwOk;
  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) onSubmit(patch);
      }}
    >
      {can("ssid") && (
        <Field label="Network name" hint={`Up to 32 bytes (Hebrew letters count as 2). Now ${ssidBytes}.`}>
          <Input value={ssid} onChange={(e) => setSsid(e.target.value)} dir="auto" autoComplete="off" />
        </Field>
      )}
      {can("encryption") && (
        <Field label="Security">
          <select className={`${selectClass} w-full`} value={security} onChange={(e) => setSecurity(e.target.value)}>
            {(securityChoices.includes(current.encryption) ? securityChoices : [current.encryption, ...securityChoices]).map((m) => (
              <option key={m} value={m}>
                {securityLabel(m)}
              </option>
            ))}
          </select>
        </Field>
      )}
      {can("password") && !open && (
        <Field label="Password" hint={securing ? "Required to secure the network. 8 to 63 characters." : "Leave empty to keep it. 8 to 63 characters."}>
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} maxLength={63} autoComplete="new-password" />
        </Field>
      )}
      {can("hidden") && <Switch checked={hidden} onChange={setHidden} label="Hide the network name" />}
      <p className="text-xs text-drift">Devices on this network disconnect and must join again with the new details.</p>
      <ErrorNote error={error} />
      <div className="flex justify-end gap-2">
        <Button type="button" onClick={onCancel}>
          Cancel
        </Button>
        <Button tone="primary" type="submit" disabled={!valid || busy}>
          Save
        </Button>
      </div>
    </form>
  );
}

export function WiFi() {
  const { data, error, isLoading } = useWifi();
  const bands = Object.keys(data?.bands ?? {}).sort((a, b) => bandOrder.indexOf(a) - bandOrder.indexOf(b));
  const nets = [...new Set(bands.flatMap((b) => Object.keys(data!.bands[b]!.networks ?? {})))].sort((a, b) => {
    const order = ["host", "guest", "iot", "mlo"];
    return (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99);
  });
  return (
    <>
      <PageTitle>Wi-Fi</PageTitle>
      {error && <ErrorNote error={error} />}
      {isLoading && <p className="text-drift">Loading…</p>}
      {data && (
        <div className="space-y-6">
          <Panel title="Networks">
            {bands.length ? (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[480px] text-sm">
                  <thead className="text-left text-xs text-drift">
                    <tr>
                      <th className="pb-2 font-normal">Network</th>
                      {bands.map((b) => (
                        <th key={b} className="pb-2 font-normal">
                          {bandLabel(b)}
                          {!data.bands[b]!.radio.enable && " (radio off)"}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {nets.map((nw) => (
                      <tr key={nw} className="border-t border-line">
                        <td className="py-2 font-medium">{networkLabel(nw)}</td>
                        {bands.map((b) => {
                          const c = data.bands[b]!.networks?.[nw];
                          return (
                            <td key={b} className="py-2">
                              {c ? (
                                <span className="flex items-center gap-2">
                                  <span aria-hidden className={c.enable ? "text-lagoon" : "text-drift"}>
                                    {c.enable ? "●" : "○"}
                                  </span>
                                  <span className="sr-only">{c.enable ? "On" : "Off"}</span>
                                  <span dir="auto" className={c.enable ? "" : "text-drift"}>
                                    {c.ssid || "—"}
                                  </span>
                                </span>
                              ) : (
                                <span className="text-drift">—</span>
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <Empty>The Deco did not report any Wi-Fi networks.</Empty>
            )}
          </Panel>
          {nets.map((nw) => (
            <NetworkPanel key={nw} data={data} nw={nw} bands={bands} />
          ))}
        </div>
      )}
    </>
  );
}
