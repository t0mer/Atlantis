import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, setCsrfToken } from "../api/client";
import type { AuthStatus } from "../api/types";
import { Button, ErrorNote, Field, Input } from "../components/ui";

/** First-run setup (create the admin account) or sign-in. */
export function Auth({ setup }: { setup: boolean }) {
  const qc = useQueryClient();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await api<AuthStatus>(setup ? "/api/v1/auth/setup" : "/api/v1/auth/login", {
        method: "POST",
        body: { username, password },
      });
      setCsrfToken(r.csrf_token);
      qc.setQueryData<AuthStatus>(["auth"], { setup_required: false, user: r.user, csrf_token: r.csrf_token });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="grid min-h-dvh place-items-center px-4">
      <form onSubmit={submit} className="w-full max-w-sm space-y-4 rounded-lg border border-line bg-shoal p-6">
        <div>
          <h1 className="text-xl font-semibold">{setup ? "Set up Atlantis" : "Sign in to Atlantis"}</h1>
          <p className="mt-1 text-sm text-drift">
            {setup
              ? "Create the account you'll use to open Atlantis. This is not your TP-Link password."
              : "Watch and manage your Deco mesh."}
          </p>
        </div>
        <Field label="Username">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required />
        </Field>
        <Field label="Password" hint={setup ? "At least 6 characters." : undefined}>
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={setup ? "new-password" : "current-password"}
            minLength={setup ? 6 : undefined}
            required
          />
        </Field>
        <ErrorNote error={error} />
        <Button tone="primary" type="submit" disabled={busy} className="w-full justify-center">
          {busy ? "Working…" : setup ? "Create account" : "Sign in"}
        </Button>
      </form>
    </main>
  );
}
