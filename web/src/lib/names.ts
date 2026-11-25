import { useClients, useNodes } from "../api/hooks";

/** Resolves an event subject (client MAC or node ID) to a display name. */
export function useNames(): (subject: string) => string {
  const { data: clients = [] } = useClients();
  const { data: nodes = [] } = useNodes();
  const m = new Map<string, string>();
  clients.forEach((c) => m.set(c.mac, c.display_name));
  nodes.forEach((n) => m.set(n.id, n.nickname || n.model));
  return (s) => m.get(s) ?? (s === "wan" ? "Internet" : "");
}
