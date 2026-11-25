import { useState } from "react";
import { useSetMainWifi, useWifi } from "../api/hooks";
import { Button, Confirm, Empty, ErrorNote, Field, Input, PageTitle, Panel, Switch, useToast } from "../components/ui";
import { bandLabel, networkLabel } from "../lib/format";

const bandOrder = ["band2_4", "band5", "band5_1", "band5_2", "band6", "band6_2", "band60"];

export function WiFi() {
  const { data, error, isLoading } = useWifi();
  const set = useSetMainWifi();
  const toast = useToast();
  const [ssid, setSsid] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState(false);
  const bands = Object.keys(data?.bands ?? {}).sort((a, b) => bandOrder.indexOf(a) - bandOrder.indexOf(b));
  const nets = [...new Set(bands.flatMap((b) => Object.keys(data!.bands[b]!.networks ?? {})))].sort((a, b) =>
    a === "host" ? -1 : b === "host" ? 1 : a.localeCompare(b),
  );
  const currentSsid = bands.map((b) => data!.bands[b]!.networks?.host?.ssid).find(Boolean) ?? "";
  const ssidBytes = new TextEncoder().encode(ssid.trim()).length;
  const valid = ssidBytes > 0 && ssidBytes <= 32 && password.length >= 8 && password.length <= 63;
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
                                  <Switch checked={c.enable} disabled label={`${networkLabel(nw)} on ${bandLabel(b)}`} />
                                  <span dir="auto" className={c.enable ? "" : "text-drift"}>
                                    {c.ssid || "—"}
                                    {c.hidden && <span className="text-xs text-drift"> (hidden)</span>}
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
                <p className="mt-3 text-xs text-drift">Turning networks on and off from Atlantis is not available yet. Use the Deco app for that.</p>
              </div>
            ) : (
              <Empty>The Deco did not report any Wi-Fi networks.</Empty>
            )}
          </Panel>

          <Panel title="Main network name and password">
            <form
              className="max-w-md space-y-3"
              onSubmit={(e) => {
                e.preventDefault();
                if (valid) setConfirm(true);
              }}
            >
              <p className="text-sm text-drift">Changes the name and password on every band of the main network.</p>
              <Field label="Network name" hint={`Up to 32 bytes (Hebrew letters count as 2). Now ${ssidBytes}.`}>
                <Input value={ssid} onChange={(e) => setSsid(e.target.value)} placeholder={currentSsid} dir="auto" autoComplete="off" />
              </Field>
              <Field label="Password" hint="8 to 63 characters.">
                <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} maxLength={63} autoComplete="new-password" />
              </Field>
              <Button tone="primary" type="submit" disabled={!valid}>
                Change Wi-Fi
              </Button>
            </form>
          </Panel>
        </div>
      )}
      <Confirm
        open={confirm}
        onOpenChange={setConfirm}
        title="Change the main Wi-Fi?"
        body={
          <>
            <p>
              Every device on the main network disconnects. Each one must join <strong dir="auto">{ssid}</strong> with the new password.
            </p>
            <p>If you're reading this over Wi-Fi, you'll lose this page too.</p>
          </>
        }
        typed="CHANGE"
        action="Change Wi-Fi"
        busy={set.isPending}
        onConfirm={() =>
          set.mutate(
            { ssid: ssid.trim(), password },
            {
              onSuccess: () => {
                setConfirm(false);
                setPassword("");
                toast(`Wi-Fi changed to ${ssid.trim()}`);
              },
              onError: (e) => toast((e as Error).message, "bad"),
            },
          )
        }
      />
    </>
  );
}
