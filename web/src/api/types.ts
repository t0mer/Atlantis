// Mirrors internal/api/openapi.yaml.

export type SessionState =
  | "idle"
  | "connected"
  | "paused"
  | "blocked_by_other_admin"
  | "auth_failed"
  | "cert_changed"
  | "cert_unknown"
  | "disconnected";

export interface SessionStatus {
  state: SessionState;
  since: string;
  error?: string;
  retry_at?: string;
  last_ok?: string;
  logins: number;
  paused_for?: string;
}

export interface Backhaul {
  type: "" | "wired" | "wifi" | "plc" | "plc+wifi" | "other";
  links: string[] | null;
  signal: Record<string, number> | null;
}

export interface Device {
  id: string;
  mac: string;
  ip: string;
  nickname: string;
  model: string;
  hardware_ver: string;
  software_ver: string;
  master: boolean;
  online: boolean;
  internet_online: boolean;
  internet_error: string;
  parent_id: string;
  backhaul: Backhaul;
}

export interface Node extends Device {
  first_seen: string;
  last_seen: string;
  clients: number;
}

export interface LiveClient {
  mac: string;
  ip: string;
  name: string;
  type: string;
  online: boolean;
  network: string;
  wired: boolean;
  band: string;
  node_id: string;
  signal_level: number;
  up_bps: number;
  down_bps: number;
}

export interface Client {
  mac: string;
  name: string;
  alias: string;
  display_name: string;
  type: string;
  watch: boolean;
  tags: string[];
  online: boolean;
  ip: string;
  node_id: string;
  band: string;
  network: string;
  wired: boolean;
  signal_level: number;
  up_bps: number;
  down_bps: number;
  first_seen: string;
  last_seen: string;
  private_mac: boolean;
}

export interface Point {
  ts: string;
  online: number;
  up_bps?: number;
  down_bps?: number;
  node_id?: string;
  band?: string;
  clients?: number;
  cpu?: number;
  mem?: number;
  internet_online?: boolean;
}

export interface NetworkConfig {
  enable: boolean;
  ssid: string;
  encryption: string;
  hidden: boolean;
}

export interface Band {
  radio: { enable: boolean; channel: string };
  networks: Record<string, NetworkConfig>;
}

export interface Wireless {
  bands: Record<string, Band>;
}

export interface Firmware {
  model: string;
  node_ids: string[] | null;
  latest_version: string;
  release_date: string;
  release_note: string;
  upgrade_available: boolean;
}

export interface WAN {
  online: boolean;
  ip: string;
  gateway: string;
  mask: string;
  dns1: string;
  dns2: string;
  dial_type: string;
  lan_ip: string;
}

export interface NetworkSummary {
  host: string;
  session: SessionStatus;
  internet_online: boolean | null;
  wan: WAN | null;
  wan_supported: boolean;
  performance: { cpu_usage: number; mem_usage: number } | null;
  performance_supported: boolean;
  nodes: { online: number; total: number };
  clients: Record<"total" | "wired" | "guest" | "band2_4" | "band5" | "band6", number>;
  updated: { fast?: string; slow?: string };
}

export type EventType =
  | "client.joined"
  | "client.online"
  | "client.offline"
  | "node.online"
  | "node.offline"
  | "wan.up"
  | "wan.down"
  | "wan.ip_changed"
  | "firmware.changed";

export interface DecoEvent {
  id: number;
  ts: string;
  type: EventType;
  subject: string;
  payload: Record<string, unknown>;
}

export interface ChannelConfig {
  url?: string;
  api_key?: string;
  chat?: string;
}

export interface Channel {
  id: number;
  kind: "shoutrrr" | "openwa";
  name: string;
  enabled: boolean;
  events: string[];
  watched_only: boolean;
  template: string;
  configured: boolean;
}

export interface Snapshot {
  nodes: Device[] | null;
  clients: LiveClient[] | null;
  wireless?: Wireless;
  firmware?: Firmware[];
  wan?: WAN;
  wan_supported: boolean;
  performance_supported: boolean;
  fast_at?: string;
  slow_at?: string;
}

export interface Settings {
  deco: { transport: string; host: string; port: number; email: string };
  poll: { fast: string; slow: string; offline_after_misses: number; source: "ui" | "config" };
  store: { raw_retention: string; rollup_retention: string };
  metrics: { enabled: boolean; path: string; per_client: string };
  notify: { quiet_hours: string; timezone: string; events: EventType[] };
  version: string;
}

export interface AuthStatus {
  setup_required: boolean;
  user?: string;
  csrf_token?: string;
}

export interface DecoStatus {
  configured: boolean;
  running: boolean;
  needs_password: boolean;
  host?: string;
  port?: number;
  transport?: "ssh" | "luci";
  email?: string;
  host_key?: string;
  discovery?: { model: string; role: string; port: number };
}

export interface DecoConnect {
  host: string;
  port?: number;
  transport?: "ssh" | "luci";
  email?: string;
  password?: string;
}
