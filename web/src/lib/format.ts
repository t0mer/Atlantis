import type { DecoEvent, SessionStatus } from "../api/types";

/** Bits per second as a short human rate. */
export function rate(bps: number): string {
  if (!bps) return "0";
  const units = ["b/s", "kb/s", "Mb/s", "Gb/s"];
  let v = bps;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** "12s ago", "5 min ago", "3 h ago", "2 days ago". */
export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const t = Date.parse(iso);
  if (Number.isNaN(t) || t < 86400000) return "never"; // the zero time
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  const d = Math.round(s / 86400);
  return `${d} day${d === 1 ? "" : "s"} ago`;
}

const bandNames: Record<string, string> = {
  band2_4: "2.4 GHz",
  band5: "5 GHz",
  band5_1: "5 GHz",
  band5_2: "5 GHz-2",
  band6: "6 GHz",
  band6_2: "6 GHz-2",
  band60: "60 GHz",
  mlo: "Multi-link",
};

export function bandLabel(band: string, wired = false): string {
  if (wired) return "Wired";
  if (!band) return "—";
  if (band.includes("+")) return "Multi-link";
  return bandNames[band] ?? band;
}

const networkNames: Record<string, string> = { main: "Main", host: "Main", guest: "Guest", iot: "IoT", mlo: "Multi-link" };
export const networkLabel = (n: string) => networkNames[n] ?? (n || "—");

const backhaulNames: Record<string, string> = {
  wired: "Ethernet",
  wifi: "Wireless",
  plc: "Powerline",
  "plc+wifi": "Powerline and wireless",
  other: "Other link",
};
export const backhaulLabel = (t: string) => backhaulNames[t] ?? "Main unit";

/** One plain sentence describing the Deco session. */
export function sessionSentence(s: SessionStatus | undefined): { text: string; tone: "ok" | "warn" | "bad" } {
  switch (s?.state) {
    case "connected":
      return { text: "Connected to the Deco", tone: "ok" };
    case "idle":
      return { text: "Connecting to the Deco", tone: "warn" };
    case "paused":
      return { text: s.paused_for ? `Session released for ${s.paused_for.replace("0s", "")} so you can use the Deco app` : "Polling paused", tone: "warn" };
    case "blocked_by_other_admin":
      return { text: "The Deco app or another admin is signed in; retrying shortly", tone: "warn" };
    case "auth_failed":
      return { text: "The Deco rejected the password. Fix it in the configuration and restart Atlantis", tone: "bad" };
    case "cert_changed":
      return { text: "The Deco's security key changed. Check it, then trust the new key", tone: "bad" };
    case "cert_unknown":
      return { text: "The Deco's security key is not trusted yet", tone: "bad" };
    case "disconnected":
      return { text: "Lost the connection to the Deco; retrying", tone: "warn" };
    default:
      return { text: "Waiting for the first poll", tone: "warn" };
  }
}

/** A short sentence for an event; name resolves a subject to a display name. */
export function eventText(e: DecoEvent, name: (subject: string) => string): string {
  const p = e.payload ?? {};
  // The sentence is English (LTR); the name may be Hebrew, so it is wrapped
  // in Unicode isolates (FSI..PDI) and can't reorder the words around it.
  const who = `\u2068${name(e.subject) || String(p.name ?? e.subject)}\u2069`;
  switch (e.type) {
    case "client.joined":
      return `${who} joined for the first time`;
    case "client.online":
      return `${who} came online`;
    case "client.offline":
      return `${who} went offline`;
    case "node.online":
      return `Deco ${who} is back online`;
    case "node.offline":
      return p.missing ? `Deco ${who} disappeared from the mesh` : `Deco ${who} went offline`;
    case "wan.up":
      return "Internet is back";
    case "wan.down":
      return p.error ? `Internet went down (${String(p.error)})` : "Internet went down";
    case "wan.ip_changed":
      return `Public IP changed from ${String(p.from)} to ${String(p.to)}`;
    case "firmware.changed":
      return `Deco ${who} updated from ${String(p.from)} to ${String(p.to)}`;
    default:
      return `${e.type} ${who}`;
  }
}

export const eventTone = (t: string): "ok" | "warn" | "bad" =>
  t.endsWith(".offline") || t === "wan.down" ? "bad" : t === "client.joined" || t === "firmware.changed" ? "warn" : "ok";

/** "168h0m0s" (a Go duration) → "7 days"; "5m0s" → "5 minutes". */
export function goDuration(d: string): string {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(d);
  if (!m || !d) return d;
  const secs = Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Number(m[3] ?? 0);
  const unit = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  if (secs % 86400 === 0) return unit(secs / 86400, "day");
  if (secs % 3600 === 0) return unit(secs / 3600, "hour");
  if (secs % 60 === 0) return unit(secs / 60, "minute");
  return unit(secs, "second");
}
