import { useState } from "react";
import type { DecoConnect, DecoStatus } from "../api/types";
import { Button, ErrorNote, Field, Input, selectClass } from "./ui";

/** The main Deco's address and TP-Link ID. With `current`, an empty
 * password keeps the stored one. */
export function DecoForm({
  current,
  busy,
  error,
  submitLabel,
  onSubmit,
  onCancel,
}: {
  current?: DecoStatus;
  busy: boolean;
  error: unknown;
  submitLabel: string;
  onSubmit: (c: DecoConnect) => void;
  onCancel?: () => void;
}) {
  const [host, setHost] = useState(current?.host ?? "");
  const [email, setEmail] = useState(current?.email ?? "");
  const [password, setPassword] = useState("");
  const [port, setPort] = useState(current?.port ? String(current.port) : "");
  const [transport, setTransport] = useState<"ssh" | "luci">(current?.transport ?? "ssh");
  const [advanced, setAdvanced] = useState(!!current?.port || current?.transport === "luci");
  // The saved password is only reused for the same Deco and account; the
  // server refuses to send it anywhere else.
  const sameTarget =
    host.trim() === (current?.host ?? "") &&
    (port ? Number(port) : 0) === (current?.port ?? 0) &&
    transport === (current?.transport ?? "ssh") &&
    email.trim().toLowerCase() === (current?.email ?? "").toLowerCase();
  const keepPassword = !!current?.configured && !current.needs_password && sameTarget;
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    onSubmit({
      host: host.trim(),
      email: email.trim(),
      password: password || undefined,
      port: port ? Number(port) : 0,
      transport,
    });
  };
  return (
    <form onSubmit={submit} className="space-y-4">
      <Field label="Main Deco's IP address" hint="The Deco connected to your modem. Find it in the Deco app under the Deco's details, or on your modem's device list.">
        <Input value={host} onChange={(e) => setHost(e.target.value)} placeholder="192.168.68.1" inputMode="decimal" autoComplete="off" required />
      </Field>
      {transport === "ssh" && (
        <Field label="TP-Link ID email" hint="The account that owns the Deco network in the Deco app.">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="username" required />
        </Field>
      )}
      <Field
        label="TP-Link ID password"
        hint={
          keepPassword
            ? "Leave empty to keep the saved password. It is stored encrypted and never shown."
            : current?.configured
              ? "Required for a new address or account. Stored encrypted and never shown again."
              : "Stored encrypted and never shown again."
        }
      >
        <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required={!keepPassword} />
      </Field>
      {advanced ? (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Connection">
            <select className={`${selectClass} w-full`} value={transport} onChange={(e) => setTransport(e.target.value as "ssh" | "luci")}>
              <option value="ssh">Deco app (SSH)</option>
              <option value="luci">Web page (older)</option>
            </select>
          </Field>
          {transport === "ssh" && (
            <Field label="Port" hint="Empty to detect it.">
              <Input value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} inputMode="numeric" placeholder="20001" />
            </Field>
          )}
        </div>
      ) : (
        <button type="button" className="text-sm text-lagoon underline-offset-2 hover:underline" onClick={() => setAdvanced(true)}>
          Show advanced options
        </button>
      )}
      <ErrorNote error={error} />
      <p className="text-xs text-drift">Atlantis signs in to the Deco to check these before saving them. The Deco app may be signed out while it does.</p>
      <div className="flex justify-end gap-2">
        {onCancel && (
          <Button type="button" onClick={onCancel}>
            Cancel
          </Button>
        )}
        <Button tone="primary" type="submit" disabled={busy}>
          {busy ? "Checking the Deco…" : submitLabel}
        </Button>
      </div>
    </form>
  );
}
