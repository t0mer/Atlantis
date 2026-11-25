-- Atlantis schema v1. Every table carries network_id so more sites can be
-- added later; one instance manages one site today.

CREATE TABLE networks (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL DEFAULT '',
    host         TEXT NOT NULL,
    transport    TEXT NOT NULL,
    email        TEXT NOT NULL DEFAULT '',
    password_enc TEXT NOT NULL DEFAULT '',
    host_key     TEXT NOT NULL DEFAULT '',
    tls_pin      TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);

CREATE TABLE nodes (
    network_id      INTEGER NOT NULL REFERENCES networks(id),
    id              TEXT NOT NULL,
    mac             TEXT NOT NULL DEFAULT '',
    nickname        TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL DEFAULT '',
    hw_ver          TEXT NOT NULL DEFAULT '',
    fw_ver          TEXT NOT NULL DEFAULT '',
    master          INTEGER NOT NULL DEFAULT 0,
    ip              TEXT NOT NULL DEFAULT '',
    parent_id       TEXT NOT NULL DEFAULT '',
    backhaul        TEXT NOT NULL DEFAULT '',
    online          INTEGER NOT NULL DEFAULT 0,
    internet_online INTEGER NOT NULL DEFAULT 0,
    first_seen      INTEGER NOT NULL,
    last_seen       INTEGER NOT NULL,
    PRIMARY KEY (network_id, id)
);

CREATE TABLE node_samples (
    network_id      INTEGER NOT NULL,
    node_id         TEXT NOT NULL,
    ts              INTEGER NOT NULL,
    online          INTEGER NOT NULL,
    internet_online INTEGER NOT NULL,
    clients         INTEGER NOT NULL,
    signal          INTEGER NOT NULL,
    cpu             REAL,
    mem             REAL
);
CREATE INDEX node_samples_ts ON node_samples (network_id, node_id, ts);

CREATE TABLE clients (
    network_id   INTEGER NOT NULL REFERENCES networks(id),
    mac          TEXT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    alias        TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL DEFAULT '',
    watch        INTEGER NOT NULL DEFAULT 0,
    tags         TEXT NOT NULL DEFAULT '[]',
    online       INTEGER NOT NULL DEFAULT 0,
    last_node    TEXT NOT NULL DEFAULT '',
    last_band    TEXT NOT NULL DEFAULT '',
    last_network TEXT NOT NULL DEFAULT '',
    last_ip      TEXT NOT NULL DEFAULT '',
    wired        INTEGER NOT NULL DEFAULT 0,
    first_seen   INTEGER NOT NULL,
    last_seen    INTEGER NOT NULL,
    PRIMARY KEY (network_id, mac)
);

CREATE TABLE client_samples (
    network_id INTEGER NOT NULL,
    mac        TEXT NOT NULL,
    ts         INTEGER NOT NULL,
    online     INTEGER NOT NULL,
    node_id    TEXT NOT NULL,
    band       TEXT NOT NULL,
    network    TEXT NOT NULL,
    ip         TEXT NOT NULL,
    up_bps     INTEGER NOT NULL,
    down_bps   INTEGER NOT NULL
);
CREATE INDEX client_samples_ts ON client_samples (network_id, mac, ts);

-- Hourly rollups kept after raw samples expire.
CREATE TABLE client_rollups (
    network_id    INTEGER NOT NULL,
    mac           TEXT NOT NULL,
    hour          INTEGER NOT NULL,
    samples       INTEGER NOT NULL,
    online_count  INTEGER NOT NULL,
    avg_up_bps    INTEGER NOT NULL,
    avg_down_bps  INTEGER NOT NULL,
    max_up_bps    INTEGER NOT NULL,
    max_down_bps  INTEGER NOT NULL,
    PRIMARY KEY (network_id, mac, hour)
);

CREATE TABLE node_rollups (
    network_id   INTEGER NOT NULL,
    node_id      TEXT NOT NULL,
    hour         INTEGER NOT NULL,
    samples      INTEGER NOT NULL,
    online_count INTEGER NOT NULL,
    avg_clients  REAL NOT NULL,
    avg_cpu      REAL,
    avg_mem      REAL,
    PRIMARY KEY (network_id, node_id, hour)
);

CREATE TABLE events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    network_id   INTEGER NOT NULL,
    ts           INTEGER NOT NULL,
    type         TEXT NOT NULL,
    subject      TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX events_ts ON events (network_id, ts);

CREATE TABLE channels (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT NOT NULL,
    name         TEXT NOT NULL,
    config_enc   TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    events_json  TEXT NOT NULL DEFAULT '[]',
    watched_only INTEGER NOT NULL DEFAULT 0,
    template     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE audit_log (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    ts     INTEGER NOT NULL,
    user   TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL,
    result TEXT NOT NULL,
    error  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value_json TEXT NOT NULL
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
