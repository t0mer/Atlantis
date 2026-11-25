import { Activity, House, Laptop, Moon, Router, Settings as Cog, Sun, Wifi, LogOut } from "lucide-react";
import { useState, type ReactNode } from "react";
import { NavLink } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { useNetwork, useSession, useSessionActions } from "../api/hooks";
import { ago, sessionSentence } from "../lib/format";
import { applyTheme, initialTheme, type Theme } from "../lib/theme";
import { Button, Confirm, cx, sessionDot, useToast } from "./ui";

const nav = [
  { to: "/", label: "Overview", icon: House, end: true },
  { to: "/clients", label: "Clients", icon: Laptop },
  { to: "/nodes", label: "Decos", icon: Router },
  { to: "/wifi", label: "Wi-Fi", icon: Wifi },
  { to: "/events", label: "Events", icon: Activity },
  { to: "/settings", label: "Settings", icon: Cog },
];

function Mark() {
  return (
    <svg viewBox="0 0 32 32" className="size-7" aria-hidden>
      <circle cx="16" cy="16" r="13" fill="none" stroke="var(--lagoon)" strokeWidth="2.5" />
      <path d="M6 18c3-3 7-3 10 0s7 3 10 0" fill="none" stroke="var(--lagoon)" strokeWidth="2.5" strokeLinecap="round" />
    </svg>
  );
}

/** The one line that says how Atlantis's link to the Deco is doing. */
function SessionLine() {
  const { data: s } = useSession();
  const { data: net } = useNetwork();
  const actions = useSessionActions();
  const toast = useToast();
  const [confirmKey, setConfirmKey] = useState(false);
  const { text, tone } = sessionSentence(s ?? net?.session);
  const state = (s ?? net?.session)?.state;
  const updated = net?.updated?.fast;
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line px-4 py-2.5 text-sm md:px-6">
      <p className="flex items-center gap-2">
        {sessionDot(tone)}
        <span className={tone === "bad" ? "text-flare" : ""}>{text}</span>
        {tone === "ok" && updated && <span className="text-drift">· updated {ago(updated)}</span>}
      </p>
      <div className="flex gap-2">
        {state === "connected" && (
          <Button
            onClick={() =>
              actions.release.mutate(10, { onSuccess: () => toast("Session released for 10 minutes. Use the Deco app now.") })
            }
            title="Sign Atlantis out of the Deco for 10 minutes so the Deco app can sign in"
          >
            Release for the app
          </Button>
        )}
        {(state === "paused" || state === "blocked_by_other_admin" || state === "disconnected") && (
          <Button tone="primary" onClick={() => actions.resume.mutate(undefined, { onSuccess: () => toast("Resumed") })}>
            Resume now
          </Button>
        )}
        {(state === "cert_changed" || state === "cert_unknown") && (
          <Button tone="danger" onClick={() => setConfirmKey(true)}>
            Trust the new key
          </Button>
        )}
      </div>
      <Confirm
        open={confirmKey}
        onOpenChange={setConfirmKey}
        title="Trust the Deco's new security key?"
        body={
          <>
            <p>Atlantis stopped because the Deco presented a different key than before. That is expected after a firmware update or reset.</p>
            <p>If neither happened, something else on your network may be answering in its place. Don't trust it.</p>
          </>
        }
        action="Trust the new key"
        busy={actions.acceptKey.isPending}
        onConfirm={() =>
          actions.acceptKey.mutate(undefined, {
            onSuccess: () => (setConfirmKey(false), toast("New key trusted")),
            onError: (e) => toast((e as Error).message, "bad"),
          })
        }
      />
    </div>
  );
}

export function Shell({ user, children }: { user?: string; children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const qc = useQueryClient();
  const toggle = () => {
    const t = theme === "dark" ? "light" : "dark";
    setTheme(t);
    applyTheme(t, true);
  };
  const logout = async () => {
    try {
      await api("/api/v1/auth/logout", { method: "POST" });
    } catch {
      /* the session may already be gone; reload to the sign-in page either way */
    }
    qc.clear();
    window.location.assign("/");
  };
  const link = ({ isActive }: { isActive: boolean }) =>
    cx(
      "flex items-center gap-3 rounded-md px-3 py-2 text-sm",
      isActive ? "bg-sea font-semibold text-abyss" : "text-drift hover:bg-sea hover:text-abyss",
    );
  return (
    <div className="min-h-dvh md:grid md:grid-cols-[13rem_1fr]">
      <aside className="hidden border-r border-line bg-shoal md:flex md:flex-col">
        <div className="flex items-center gap-2 px-4 py-4">
          <Mark />
          <span className="text-lg font-semibold tracking-tight">Atlantis</span>
        </div>
        <nav className="flex-1 space-y-0.5 px-2">
          {nav.map(({ to, label, icon: Icon, end }) => (
            <NavLink key={to} to={to} end={end} className={link}>
              <Icon size={17} aria-hidden /> {label}
            </NavLink>
          ))}
        </nav>
        <div className="space-y-1 border-t border-line p-2 text-sm">
          <button onClick={toggle} className="flex w-full items-center gap-3 rounded-md px-3 py-2 text-drift hover:bg-sea hover:text-abyss">
            {theme === "dark" ? <Sun size={17} aria-hidden /> : <Moon size={17} aria-hidden />}
            {theme === "dark" ? "Light mode" : "Dark mode"}
          </button>
          <button onClick={logout} className="flex w-full items-center gap-3 rounded-md px-3 py-2 text-drift hover:bg-sea hover:text-abyss">
            <LogOut size={17} aria-hidden /> Sign out{user ? ` ${user}` : ""}
          </button>
        </div>
      </aside>
      <div className="flex min-w-0 flex-col pb-16 md:pb-0">
        <div className="flex items-center justify-between border-b border-line bg-shoal px-4 py-3 md:hidden">
          <span className="flex items-center gap-2 font-semibold">
            <Mark /> Atlantis
          </span>
          <button onClick={toggle} aria-label={theme === "dark" ? "Light mode" : "Dark mode"} className="p-1 text-drift">
            {theme === "dark" ? <Sun size={19} /> : <Moon size={19} />}
          </button>
        </div>
        <SessionLine />
        <main className="mx-auto w-full max-w-[1100px] px-4 py-6 md:px-6">{children}</main>
      </div>
      <nav aria-label="Main" className="fixed inset-x-0 bottom-0 z-40 grid grid-cols-6 border-t border-line bg-shoal md:hidden">
        {nav.map(({ to, label, icon: Icon, end }) => (
          <NavLink
            key={to}
            to={to}
            end={end}
            className={({ isActive }) => cx("flex flex-col items-center gap-0.5 py-2 text-[11px]", isActive ? "text-lagoon" : "text-drift")}
          >
            <Icon size={19} aria-hidden />
            {label}
          </NavLink>
        ))}
      </nav>
    </div>
  );
}
