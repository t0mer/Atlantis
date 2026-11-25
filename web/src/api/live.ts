import { useEffect, useSyncExternalStore } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { DecoEvent, SessionStatus } from "./types";

// Subjects (client MACs, node IDs) that changed recently, so their marker
// can pulse once. A tiny external store keeps this outside React state.
const recent = new Map<string, number>();
const listeners = new Set<() => void>();
let version = 0;
function bump(subject: string) {
  recent.set(subject, Date.now());
  version++;
  listeners.forEach((l) => l());
}
export function useRecentlyChanged(subject: string): boolean {
  useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => version,
  );
  const t = recent.get(subject);
  return t !== undefined && Date.now() - t < 1500;
}

/** Applies one stream message to the query cache. Exported for tests. */
export function applyMessage(qc: QueryClient, kind: string, data: unknown) {
  switch (kind) {
    case "snapshot":
      // "node" covers node detail pages and the overview's strips.
      ["network", "nodes", "node", "wifi", "firmware"].forEach((k) => qc.invalidateQueries({ queryKey: [k] }));
      break;
    case "clients":
      // "client" covers an open client page; list sparklines use "spark"
      // and are left to go stale on their own.
      ["clients", "client", "network"].forEach((k) => qc.invalidateQueries({ queryKey: [k] }));
      break;
    case "event": {
      const e = data as DecoEvent;
      qc.setQueryData<DecoEvent[]>(["events", ""], (old) => (old && !old.some((x) => x.id === e.id) ? [e, ...old] : old));
      bump(e.subject);
      (e.type.startsWith("node.") ? ["nodes", "node"] : ["clients", "client"]).forEach((k) => qc.invalidateQueries({ queryKey: [k] }));
      break;
    }
    case "deco":
      // The Deco was added, changed or removed: everything is different.
      qc.invalidateQueries();
      break;
    case "session":
      qc.setQueryData<SessionStatus>(["session"], data as SessionStatus);
      qc.invalidateQueries({ queryKey: ["network"] });
      break;
  }
}

/** Keeps one EventSource open while signed in; the browser reconnects. */
export function useLiveStream(enabled: boolean) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const es = new EventSource("/api/v1/stream");
    const kinds = ["snapshot", "clients", "event", "session", "deco"];
    const handlers = kinds.map((k) => {
      const h = (m: MessageEvent<string>) => {
        try {
          applyMessage(qc, k, JSON.parse(m.data));
        } catch {
          /* ignore a malformed message */
        }
      };
      es.addEventListener(k, h);
      return [k, h] as const;
    });
    return () => {
      handlers.forEach(([k, h]) => es.removeEventListener(k, h));
      es.close();
    };
  }, [enabled, qc]);
}
