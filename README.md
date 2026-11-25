# Atlantis

[![CI](https://github.com/t0mer/Atlantis/actions/workflows/ci.yml/badge.svg)](https://github.com/t0mer/Atlantis/actions/workflows/ci.yml)
[![Security](https://github.com/t0mer/Atlantis/actions/workflows/security.yml/badge.svg)](https://github.com/t0mer/Atlantis/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Atlantis is a self-hosted web app that monitors and manages a TP-Link Deco mesh from your own network, without the Deco mobile app. It shows which Decos and devices are online, how they connect, and who came and went. It keeps that history, sends notifications, and exports Prometheus metrics.

It is a single Go binary with the web UI built in. It runs on a Raspberry Pi, a NAS or any Docker host on the same LAN as your main Deco.

> Atlantis is an independent project. It is not affiliated with, endorsed by or supported by TP-Link.

![Overview](assets/screenshots/overview.png)

## Contents

- [Features](#features)
- [Screenshots](#screenshots)
- [How it works](#how-it-works)
- [Requirements](#requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [Using Atlantis](#using-atlantis)
- [Notifications](#notifications)
- [Prometheus metrics](#prometheus-metrics)
- [REST API](#rest-api)
- [Command line](#command-line)
- [Security](#security)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [License](#license)

## Features

- **Mesh at a glance.** See whether the internet is up and how many Decos and devices are online. The mesh is drawn as a tree that shows how each satellite connects back (wired or wireless, with signal bars).
- **24-hour presence strips** for every Deco and device. They show when each one was online, with changes marked.
- **Clients.** Search and filter by online state, Deco, band, network (main, guest, IoT) and watched. Each device shows its current speed, a 24-hour strip and a detail page with presence, throughput and connection history over 24 hours, 7 days or 30 days.
- **Names that stay yours.** Give a device a name, tags and a "watch" flag. These are stored in Atlantis; the Deco is not changed.
- **Decos.** Model, firmware (with a notice when an update is available), IP and MAC, role, backhaul, and the devices connected to each one.
- **Wi-Fi.** Every band and network with its name and state. You can change the main network's name and password on all bands, after typing `CHANGE` to confirm.
- **Events.** A timeline of devices joining, coming and going, Decos going offline, internet outages, public IP changes and firmware updates.
- **Notifications** through [Shoutrrr](https://containrrr.dev/shoutrrr/) (Telegram, Discord, Slack, ntfy, email and more) or WhatsApp through [OpenWA](https://github.com/open-wa/wa-automate-nodejs). Each channel picks which events it sends and can be limited to watched devices. Messages use templates, and there are quiet hours.
- **Prometheus metrics** for Decos, clients, the internet connection and Atlantis's own polling.
- **History in SQLite**, with raw samples rolled up hourly. You choose how long both are kept.
- **Shares the Deco politely.** The Deco allows one admin session at a time. Atlantis can release it for a few minutes so you can use the Deco app.
- Live updates, light and dark mode, a phone layout, and correct display of Hebrew and other right-to-left names.

## Screenshots

### Clients

![Clients](assets/screenshots/clients.png)

### Client detail

![Client detail](assets/screenshots/client-detail.png)

### Decos

![Decos](assets/screenshots/nodes.png)

### Deco detail

![Deco detail](assets/screenshots/node-detail.png)

### Wi-Fi

![Wi-Fi](assets/screenshots/wifi.png)

Changing the main network asks for a typed confirmation:

![Wi-Fi change confirmation](assets/screenshots/wifi-confirm.png)

### Events

![Events](assets/screenshots/events.png)

### Settings and notifications

![Settings](assets/screenshots/settings.png)

![Add a notification channel](assets/screenshots/channel-dialog.png)

### Dark mode

![Overview, dark](assets/screenshots/overview-dark.png)

![Clients, dark](assets/screenshots/clients-dark.png)

### Phone

<p>
  <img src="assets/screenshots/mobile-overview.png" alt="Overview on a phone" width="300">
  <img src="assets/screenshots/mobile-dark.png" alt="Overview on a phone, dark" width="300">
</p>

The screenshots use a pretend mesh that ships with the source (see [Development](#development)), not a real network.

## How it works

```mermaid
flowchart LR
  subgraph LAN
    Deco["Main Deco"]
    A["Atlantis<br/>(poller, store, API, UI)"]
    DB[("SQLite<br/>/data/atlantis.db")]
  end
  Browser -->|"HTTP :8080"| A
  Prom["Prometheus"] -->|"/metrics"| A
  A -->|"SSH (port 20001), the Deco app's protocol"| Deco
  A --- DB
  A -->|"Shoutrrr / OpenWA"| Notify["Telegram, WhatsApp, ..."]
```

Atlantis talks to the **main Deco** (the one in router mode). The default `ssh` transport uses the same local protocol as the Deco app: TP-Link's TMP over SSH on port 20001. It signs in with your TP-Link ID. A second transport, `luci`, uses the Deco's local web API and its local management password.

Atlantis polls the Deco on two loops:

- **Fast loop** (default every 30 seconds): clients and their speeds.
- **Slow loop** (default every 5 minutes): Decos, the internet connection, Wi-Fi and firmware.

Each result is stored and compared with the previous one. The changes become events, which go to the event log, to notifications and to every open browser over a live stream.

**One admin at a time.** A Deco accepts one admin session. While Atlantis is signed in, the Deco app may be signed out, and the reverse. When that happens, Atlantis backs off and shows the state at the top of every page. **Release for the app** signs Atlantis out for 10 minutes (or an hour from Settings) and then signs back in by itself.

## Requirements

- A TP-Link Deco mesh whose main unit is reachable from the machine running Atlantis. Atlantis has been tested with a Deco M5 on firmware 1.9.1.
- For the `ssh` transport (the default): the **TP-Link ID** (email and password) that owns the Deco network in the Deco app.
- Docker, or a Linux machine (amd64, arm64 or armv7) for the binary.

**Finding the Deco's address.** It is your network's default gateway: in the Deco app, open the main Deco and look at its IP address, or run `ip route | grep default` on a Linux machine. Many networks use `192.168.68.1`.

## Installation

Atlantis needs a secret key that encrypts the Deco password and the notification credentials stored in its database. Generate one once and keep it: without the same key, stored credentials can't be decrypted.

```sh
docker run --rm techblog/atlantis keygen
```

### Docker Compose

Save [`docker-compose.yml`](docker-compose.yml), set the Deco address in it, and put the secrets in a `.env` file next to it:

```sh
ATLANTIS_SECRET_KEY=<output of keygen>
ATLANTIS_DECO_EMAIL=you@example.com
ATLANTIS_DECO_PASSWORD=<your TP-Link ID password>
```

```sh
docker compose up -d
```

Open `http://<host>:8080` and create the admin account. That account is for Atlantis itself and is not your TP-Link ID.

### Docker

```sh
docker run -d --name atlantis -p 8080:8080 -v atlantis-data:/data \
  -e ATLANTIS_SECRET_KEY=... \
  -e ATLANTIS_DECO_HOST=192.168.68.1 \
  -e ATLANTIS_DECO_EMAIL=you@example.com \
  -e ATLANTIS_DECO_PASSWORD=... \
  --restart unless-stopped techblog/atlantis:latest
```

The image (`linux/amd64`, `linux/arm64`, `linux/arm/v7`) is built from scratch: no shell, a non-root user (UID 65532), the database on the `/data` volume, and a health check that runs `atlantis healthcheck`.

### Binary

Download the archive for your platform from [Releases](https://github.com/t0mer/Atlantis/releases). Then:

```sh
tar xzf atlantis_*_linux_arm64.tar.gz
export ATLANTIS_SECRET_KEY="$(./atlantis keygen)"   # store this somewhere safe
ATLANTIS_STORE_PATH=./atlantis.db ATLANTIS_DECO_HOST=192.168.68.1 \
ATLANTIS_DECO_EMAIL=you@example.com ATLANTIS_DECO_PASSWORD=... \
  ./atlantis serve
```

### From source

Requires Go (the version in `go.mod`) and Node.js 24.

```sh
git clone https://github.com/t0mer/Atlantis.git && cd Atlantis
make web build        # builds the UI, then bin/atlantis with the UI built in
```

## Configuration

Settings come from environment variables, an optional YAML file, and built-in defaults. **Environment variables win over the YAML file.** The YAML file is passed with `atlantis serve --config atlantis.yaml` or `ATLANTIS_CONFIG`. Each environment variable is `ATLANTIS_` followed by the key in capitals, with dots replaced by underscores. For example, `deco.host` becomes `ATLANTIS_DECO_HOST`.

The Deco password is encrypted into the database on the first start. After that you can remove it from the environment or the file; set it again only to change it.

| Key | Environment variable | Default | Description |
|---|---|---|---|
| `secret_key` | `ATLANTIS_SECRET_KEY` | **required** | Base64 of 32 bytes, from `atlantis keygen`. Encrypts stored credentials. |
| `server.listen` | `ATLANTIS_SERVER_LISTEN` | `:8080` | Address and port to listen on. |
| `server.base_url` | `ATLANTIS_SERVER_BASE_URL` | | The public URL. An `https://` URL makes the session cookie `Secure`. |
| `server.trusted_proxies` | `ATLANTIS_SERVER_TRUSTED_PROXIES` | | Reverse proxies whose forwarded client address is trusted. |
| `deco.transport` | `ATLANTIS_DECO_TRANSPORT` | `ssh` | `ssh` (the Deco app's protocol) or `luci` (the local web API). |
| `deco.host` | `ATLANTIS_DECO_HOST` | **required** | The main Deco's IP address or host name. |
| `deco.port` | `ATLANTIS_DECO_PORT` | `0` | SSH port. `0` asks the Deco (TDP discovery), then falls back to `20001`. |
| `deco.email` | `ATLANTIS_DECO_EMAIL` | required for `ssh` | Your TP-Link ID email. |
| `deco.password` | `ATLANTIS_DECO_PASSWORD` | required on first start | TP-Link ID password (`ssh`), or the local management password (`luci`). |
| `deco.host_key` | `ATLANTIS_DECO_HOST_KEY` | | Pin the Deco's SSH host key (`SHA256:...`). If empty, Atlantis trusts the key it sees first. |
| `deco.tls_pin` | `ATLANTIS_DECO_TLS_PIN` | | Pin the Deco's HTTPS certificate (`luci` only). If empty, Atlantis trusts the certificate it sees first. |
| `poll.fast` | `ATLANTIS_POLL_FAST` | `30s` | How often clients are read (minimum `10s`). |
| `poll.slow` | `ATLANTIS_POLL_SLOW` | `5m` | How often Decos, the internet connection, Wi-Fi and firmware are read (minimum `10s`). |
| `poll.offline_after_misses` | `ATLANTIS_POLL_OFFLINE_AFTER_MISSES` | `2` | Missed fast polls before a device counts as offline. |
| `store.path` | `ATLANTIS_STORE_PATH` | `/data/atlantis.db` | SQLite database file. |
| `store.raw_retention` | `ATLANTIS_STORE_RAW_RETENTION` | `168h` | How long raw samples are kept before being rolled up hourly. |
| `store.rollup_retention` | `ATLANTIS_STORE_ROLLUP_RETENTION` | `2160h` | How long hourly history and events are kept. Must be at least `raw_retention`. |
| `metrics.enabled` | `ATLANTIS_METRICS_ENABLED` | `true` | Serve Prometheus metrics. |
| `metrics.path` | `ATLANTIS_METRICS_PATH` | `/metrics` | Metrics path. |
| `metrics.per_client` | `ATLANTIS_METRICS_PER_CLIENT` | `watched` | Per-device series for `all`, `watched` or `none` of the devices. |
| `metrics.token` | `ATLANTIS_METRICS_TOKEN` | | Bearer token that Prometheus sends. Without one, metrics require a signed-in browser session. |
| `notify.quiet_hours` | `ATLANTIS_NOTIFY_QUIET_HOURS` | | `HH:MM-HH:MM` (may cross midnight). No notifications are sent in this window. |
| `notify.timezone` | `ATLANTIS_NOTIFY_TIMEZONE` | `UTC` | Time zone for quiet hours and message times, such as `Asia/Jerusalem`. |
| `openwa.url` | `ATLANTIS_OPENWA_URL` | | Default OpenWA server for WhatsApp channels. |
| `openwa.api_key` | `ATLANTIS_OPENWA_API_KEY` | | Default OpenWA API key. |
| `openwa.default_chat` | `ATLANTIS_OPENWA_DEFAULT_CHAT` | | Default WhatsApp chat (contact or group). |
| `debug.enabled` | `ATLANTIS_DEBUG_ENABLED` | `false` | Enables `GET /api/v1/debug/raw` for raw Deco responses. |
| `log.level` | `ATLANTIS_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `log.format` | `ATLANTIS_LOG_FORMAT` | `json` | `json` or `text`. |

Durations use Go's format: `30s`, `5m`, `168h`.

Example `atlantis.yaml`:

```yaml
deco:
  host: 192.168.68.1
  email: you@example.com
poll:
  fast: 30s
  slow: 5m
store:
  path: /data/atlantis.db
metrics:
  per_client: watched
  token: change-me
notify:
  quiet_hours: "23:00-07:00"
  timezone: Asia/Jerusalem
```

The Settings page shows the active configuration. To change it, edit the file or environment and restart Atlantis.

## Using Atlantis

- **First run:** create the admin account (the password needs at least 10 characters). Atlantis then signs in to the Deco. The first time, it trusts the Deco's SSH key and remembers it.
- **The status line** at the top of every page shows the connection to the Deco and its action:
  - **Release for the app:** sign Atlantis out for 10 minutes so you can use the Deco app.
  - **Resume now:** sign back in early, after a release, a pause, or another admin taking the session.
  - **Trust the new key:** shown when the Deco presents a different key than before. That is expected after a firmware update or reset; otherwise, don't trust it.
- **Settings** has the same session controls, plus **Release for an hour** and **Pause polling**, the notification channels and the active configuration.
- **Watching a device** (on its detail page) can limit notifications to the devices you care about. With `metrics.per_client: watched`, watched devices also get their own metrics.
- **Not available yet:** restarting Decos and turning individual networks on or off. Both appear in the UI but are disabled until they are verified on real hardware. Use the Deco app for those.

## Notifications

Add channels under **Settings → Notifications**. Each channel has:

- **A service.**
  - **Shoutrrr:** one URL, such as `telegram://token@telegram?chats=@channel`, `discord://token@id`, `ntfy://ntfy.sh/topic` or `smtp://...`. See the [Shoutrrr services](https://containrrr.dev/shoutrrr/latest/services/overview/).
  - **WhatsApp (OpenWA):** the server URL, API key and chat. Empty fields fall back to the `openwa.*` settings.
- **The events to send:**
  - a new device joining for the first time;
  - a device coming online or going offline;
  - a Deco going offline or coming back;
  - the internet going down or coming back;
  - the public IP changing;
  - a Deco's firmware changing.
- **Only for watched devices**, which limits the device events to watched devices.
- **A message template** (Go `text/template`) with `.Type`, `.Subject`, `.Name`, `.Site`, `.Time` and `.Payload`. Leave it empty for the built-in message.
- **Send test**, which sends a real message with the values in the form before you save.

Channel credentials are encrypted in the database and are never sent back to the browser.

## Prometheus metrics

| Metric | Labels | Meaning |
|---|---|---|
| `deco_node_up` | `mac`, `name`, `model` | 1 when the Deco is connected to the mesh. |
| `deco_clients` | `node`, `band`, `interface` | Online clients per Deco, band and network. |
| `deco_client_up` | `mac`, `name` | 1 when the device is online. |
| `deco_client_rx_bytes_rate` | `mac`, `name` | Device download rate, bytes per second. |
| `deco_client_tx_bytes_rate` | `mac`, `name` | Device upload rate, bytes per second. |
| `deco_cpu_usage` | | Main Deco CPU load, 0–1, when the model reports it. |
| `deco_mem_usage` | | Main Deco memory use, 0–1, when the model reports it. |
| `deco_wan_up` | | 1 when the main Deco reports working internet. |
| `deco_poll_duration_seconds` | `loop` | Duration of each poll (histogram). |
| `deco_poll_errors_total` | `loop`, `reason` | Failed polls. |
| `deco_session_logins_total` | `result` | Sign-ins to the Deco. |

The per-device series follow `metrics.per_client`. Go runtime and process metrics are included too.

```yaml
scrape_configs:
  - job_name: atlantis
    authorization:
      credentials: change-me   # metrics.token
    static_configs:
      - targets: ["atlantis:8080"]
```

## REST API

The UI uses the REST API under `/api/v1`. The OpenAPI spec is at `/api/openapi.yaml`, and Swagger UI is at `/api/docs`.

- **No sign-in needed:**
  - `GET /health`;
  - the sign-in endpoints (`/api/v1/auth/status`, `/auth/setup`, `/auth/login`, `/auth/logout`).
- **Everything else** needs a signed-in session: the `atlantis_session` cookie (HttpOnly, SameSite=Strict, valid for 7 days). Every request that changes something must also send the `X-CSRF-Token` header returned by `/api/v1/auth/status`, with a `Content-Type: application/json` body.
- **Sign-in lockout:** after 5 failed attempts in 15 minutes, a username or address is locked out.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/network` | Summary: internet, Decos, client counts, session state |
| `GET` | `/api/v1/nodes`, `/api/v1/nodes/{id}?range=24h\|7d\|30d` | Decos, and one Deco with its history |
| `GET` | `/api/v1/clients?q=&online=&node=&band=&network=` | Devices |
| `GET` / `PATCH` | `/api/v1/clients/{mac}` | One device with history; set `alias`, `watch`, `tags` |
| `GET` / `PATCH` | `/api/v1/wifi` | Wi-Fi; `{"network":"host","ssid":"...","password":"..."}` changes the main network (`ssh` transport only) |
| `GET` | `/api/v1/firmware` | Available firmware updates |
| `GET` | `/api/v1/events?type=&since=&limit=` | The event log |
| `GET` | `/api/v1/settings` | The active configuration |
| `GET` / `POST` / `PUT` / `DELETE` | `/api/v1/channels[/{id}]` | Notification channels |
| `POST` | `/api/v1/channels/test`, `/api/v1/channels/{id}/test` | Send a test message |
| `GET` | `/api/v1/session` | The Deco session state |
| `POST` | `/api/v1/session/logout?minutes=10`, `/pause`, `/resume`, `/accept-host-key` | Release, pause, resume, trust a new key |
| `GET` | `/api/v1/stream` | Server-sent events: `snapshot`, `clients`, `event`, `session` |
| `GET` | `/api/v1/ready` | 503 until a recent mesh poll succeeded |
| `GET` | `/api/v1/audit` | The last 200 changes made through Atlantis |

`POST /api/v1/nodes/{id}/reboot`, `POST /api/v1/nodes/reboot-all` and `PUT /api/v1/settings` exist but answer `501` until they are verified on a Deco.

## Command line

| Command | Purpose |
|---|---|
| `atlantis serve [--config FILE]` | Run the server. |
| `atlantis keygen` | Print a new secret key. |
| `atlantis healthcheck [--url URL]` | Exit 0 when the local server answers `/health` (used by the Docker health check). |
| `atlantis version` | Print the version. |
| `atlantis ssh probe --host IP --email EMAIL` | Sign in over SSH and print what the Deco reports. Read-only. The password comes from `ATLANTIS_CLOUD_PASSWORD`. |
| `atlantis ssh set-wifi --host IP --ssid NAME` | Change the main Wi-Fi name and password on every band. The new password comes from `ATLANTIS_WIFI_PASSWORD`. |
| `atlantis probe --host IP` | Sign in to the local web API (`luci`) and print what it reports. Read-only. |
| `atlantis cloud probe --email EMAIL` | List your sites through the TP-Link cloud. Read-only. |

Run any command with `--help` for all its flags.

## Security

- The Deco password and notification credentials are encrypted at rest with AES-256-GCM, using `ATLANTIS_SECRET_KEY`. They are never returned by the API and never logged.
- The Deco's SSH host key (or HTTPS certificate with `luci`) is pinned on first use. A different key stops Atlantis until you trust it from the UI.
- Atlantis's own account uses a bcrypt password, an HttpOnly SameSite=Strict session cookie, CSRF tokens on changes, and a lockout after repeated failed sign-ins.
- Expose Atlantis only on your LAN or behind a reverse proxy with HTTPS. Set `server.base_url` to the `https://` address so the cookie is marked `Secure`.
- The database holds your network's history and the encrypted credentials. Back up `/data` and the secret key together.

## Troubleshooting

- **"The Deco app or another admin is signed in; retrying shortly".** The Deco app (or a browser) holds the Deco's single admin session. Close the app, or wait: Atlantis backs off and tries again. Use **Release for the app** whenever you need the app.
- **"The Deco rejected the password".** Atlantis stops trying, to avoid locking the account. Set `ATLANTIS_DECO_PASSWORD` to the right TP-Link ID password and restart.
- **"The Deco's security key changed".** This is expected after a firmware update or factory reset. If neither happened, don't trust the new key: something else may be answering at the Deco's address.
- **CPU and memory show "—".** Some models, such as the M5, don't report them.
- **Swagger UI is blank.** `/api/docs` loads Swagger UI from unpkg.com, so the browser needs internet access. The spec itself is at `/api/openapi.yaml`.

## Development

```sh
make dev       # the UI with hot reload against a pretend Deco mesh (http://localhost:5173)
make test      # Go tests (with -race) and the UI unit tests
make lint      # go vet, golangci-lint, TypeScript and ESLint
make e2e       # Playwright smoke tests against atlantis serve and the pretend mesh
make docker    # build the image for this machine
```

`cmd/atlantis-fake` is a pretend mesh for development and tests: an SSH server speaking the Deco app's protocol, with three Decos and fourteen devices, including Hebrew names. It never talks to real hardware. `SEED=1 make e2e` starts it with a day of history; `SCREENSHOTS=dir` saves the screenshots used in this README.

```
cmd/atlantis/        the binary: serve, probes, keygen, healthcheck
cmd/atlantis-fake/   the pretend mesh
internal/deco/       the Deco client interface; tmp/ and tmpclient/ (SSH), luci/ (web API), cloud/
internal/poller/     the fast and slow loops
internal/events/     turns poll differences into events
internal/session/    the Deco session: sign-in, back-off, release, key trust
internal/store/      SQLite storage, migrations, retention
internal/notify/     Shoutrrr and OpenWA senders
internal/metrics/    Prometheus collectors
internal/api/        REST API, live stream, auth, OpenAPI spec
internal/web/        serves the built UI
web/                 the React + TypeScript UI (Vite, Tailwind)
```

Releases are cut by the **Release** workflow (GoReleaser, versions `YYYY.M.PATCH`). The **Docker** workflow then publishes `techblog/atlantis`.

## Contributing

Issues and pull requests are welcome. Before opening a pull request, run `make lint test e2e`. Never include real MAC addresses, IP addresses, network names or credentials in tests or fixtures.

## License

[Apache License 2.0](LICENSE)
