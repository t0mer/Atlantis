import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type {
  AuthStatus,
  Channel,
  ChannelConfig,
  Client,
  DecoConnect,
  DecoEvent,
  DecoStatus,
  Firmware,
  NetworkSummary,
  Node,
  Point,
  SessionStatus,
  Settings,
  SettingsPatch,
  Wireless,
  WirelessPatch,
} from "./types";

export type Range = "24h" | "7d" | "30d";

// Live data refreshes over SSE; this interval is only a fallback.
const fallback = { refetchInterval: 60_000 };

export const useAuth = () => useQuery({ queryKey: ["auth"], queryFn: () => api<AuthStatus>("/api/v1/auth/status"), staleTime: Infinity });
export const useNetwork = () => useQuery({ queryKey: ["network"], queryFn: () => api<NetworkSummary>("/api/v1/network"), ...fallback });
export const useNodes = () => useQuery({ queryKey: ["nodes"], queryFn: () => api<Node[]>("/api/v1/nodes"), ...fallback });
export const useClients = () => useQuery({ queryKey: ["clients"], queryFn: () => api<Client[]>("/api/v1/clients"), ...fallback });
export const useWifi = () => useQuery({ queryKey: ["wifi"], queryFn: () => api<Wireless>("/api/v1/wifi"), ...fallback, retry: 1 });
export const useFirmware = () => useQuery({ queryKey: ["firmware"], queryFn: () => api<Firmware[]>("/api/v1/firmware"), ...fallback });
export const useSettings = () => useQuery({ queryKey: ["settings"], queryFn: () => api<Settings>("/api/v1/settings") });
export const useChannels = () => useQuery({ queryKey: ["channels"], queryFn: () => api<Channel[]>("/api/v1/channels") });
export const useDeco = () => useQuery({ queryKey: ["deco"], queryFn: () => api<DecoStatus>("/api/v1/deco") });
export const useSession = () => useQuery({ queryKey: ["session"], queryFn: () => api<SessionStatus>("/api/v1/session"), ...fallback });

export const useEvents = (since?: string) =>
  useQuery({
    queryKey: ["events", since ?? ""],
    queryFn: () => api<DecoEvent[]>(`/api/v1/events?limit=500${since ? `&since=${encodeURIComponent(since)}` : ""}`),
    ...fallback,
  });

export const useClient = (mac: string, range: Range) =>
  useQuery({
    queryKey: ["client", mac, range],
    queryFn: () =>
      api<{ client: Client; history: Point[] | null; can_rename_on_deco?: boolean }>(`/api/v1/clients/${encodeURIComponent(mac)}?range=${range}`),
  });

export const useNode = (id: string, range: Range) =>
  useQuery({
    queryKey: ["node", id, range],
    queryFn: () => api<{ node: Node; history: Point[] | null }>(`/api/v1/nodes/${encodeURIComponent(id)}?range=${range}`),
  });

function useInvalidate(...keys: string[][]) {
  const qc = useQueryClient();
  return () => keys.forEach((k) => qc.invalidateQueries({ queryKey: k }));
}

export function useUpdateClient(mac: string) {
  const done = useInvalidate(["clients"], ["client", mac]);
  return useMutation({
    mutationFn: (body: { alias?: string; watch?: boolean; tags?: string[]; local_only?: boolean }) =>
      api<{ deco_renamed?: boolean }>(`/api/v1/clients/${encodeURIComponent(mac)}`, { method: "PATCH", body }),
    onSuccess: done,
  });
}

export function useSetWifi() {
  const done = useInvalidate(["wifi"]);
  return useMutation({
    mutationFn: (p: WirelessPatch) => api<{ network: string; bands: string[] }>("/api/v1/wifi", { method: "PATCH", body: p }),
    onSuccess: done,
  });
}

function sessionAction(path: string) {
  return () => api<SessionStatus>(path, { method: "POST" });
}

export function useSessionActions() {
  const done = useInvalidate(["session"], ["network"]);
  return {
    release: useMutation({ mutationFn: (minutes: number) => sessionAction(`/api/v1/session/logout?minutes=${minutes}`)(), onSuccess: done }),
    pause: useMutation({ mutationFn: sessionAction("/api/v1/session/pause"), onSuccess: done }),
    resume: useMutation({ mutationFn: sessionAction("/api/v1/session/resume"), onSuccess: done }),
    acceptKey: useMutation({ mutationFn: sessionAction("/api/v1/session/accept-host-key"), onSuccess: done }),
  };
}

export interface ChannelInput {
  kind: Channel["kind"];
  name: string;
  enabled?: boolean;
  events: string[];
  watched_only: boolean;
  template: string;
  config?: ChannelConfig;
}

export function useChannelMutations() {
  const done = useInvalidate(["channels"]);
  return {
    create: useMutation({ mutationFn: (c: ChannelInput) => api<Channel>("/api/v1/channels", { method: "POST", body: c }), onSuccess: done }),
    update: useMutation({
      mutationFn: ({ id, ...c }: Partial<ChannelInput> & { id: number }) => api<Channel>(`/api/v1/channels/${id}`, { method: "PUT", body: c }),
      onSuccess: done,
    }),
    remove: useMutation({ mutationFn: (id: number) => api(`/api/v1/channels/${id}`, { method: "DELETE" }), onSuccess: done }),
    testSaved: useMutation({ mutationFn: (id: number) => api<{ ok: boolean; error?: string }>(`/api/v1/channels/${id}/test`, { method: "POST" }) }),
    testDraft: useMutation({ mutationFn: (c: ChannelInput) => api<{ ok: boolean; error?: string }>("/api/v1/channels/test", { method: "POST", body: c }) }),
  };
}

/** Adds the Deco, changes its connection, or removes it. Any of these
 * changes every page, so all cached data is refetched. */
export function useDecoMutations() {
  const qc = useQueryClient();
  const done = () => qc.invalidateQueries();
  return {
    connect: useMutation({ mutationFn: (c: DecoConnect) => api<DecoStatus>("/api/v1/deco", { method: "POST", body: c }), onSuccess: done }),
    // A failed change may have left nothing running: refetch either way.
    change: useMutation({ mutationFn: (c: DecoConnect) => api<DecoStatus>("/api/v1/deco", { method: "PUT", body: c }), onSettled: done }),
    remove: useMutation({ mutationFn: () => api("/api/v1/deco", { method: "DELETE", body: { confirm: "REMOVE" } }), onSuccess: done }),
  };
}

/** Saves any settings sections; the response is the settings now in effect. */
export function useSaveSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: SettingsPatch) => api<Settings>("/api/v1/settings", { method: "PUT", body: patch }),
    onSuccess: (s) => qc.setQueryData(["settings"], s),
  });
}
