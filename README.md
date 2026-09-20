# Tab Router

Terminal-driven, Chromium-based browser for Windows and macOS that runs
several **persistent, isolated browsing identities** at once. Each identity
has its own browser profile, its own device environment (locale, timezone,
window, downloads) and its own network route with its own public IP.
Traffic from one identity can never fall back to another route or to the
host's normal connection: if a route is down, that identity is blocked.

Status: **v0.1 — two-identity milestone.** The identity count is capped at
2 until the isolation suite has been signed off (see
[`docs/ENGINEERING_PLAN.md`](docs/ENGINEERING_PLAN.md), M9).

## How it works

```
 tab-router (controller)
 ├─ Identity 001 ── Chromium #1 ──proxy──▶ Gate 001 (127.0.0.1:p1) ──▶ Route 001 ──▶ upstream A ──▶ internet as IP-A
 └─ Identity 002 ── Chromium #2 ──proxy──▶ Gate 002 (127.0.0.1:p2) ──▶ Route 002 ──▶ upstream B ──▶ internet as IP-B
```

* One Chromium process tree per identity, launched with a hardened flag set
  and `--proxy-server` pointing at a **gate** the controller owns.
* The gate is a local SOCKS5 server that forwards only while its route is
  verified `READY`. When the route fails the gate refuses every request, so
  the browser shows a connection error instead of using the host network.
  Chromium never sees upstream credentials.
* DNS is delegated to the upstream (`socks5h` semantics); the browser never
  resolves hostnames locally.
* At startup nine checks (V1–V9) prove the isolation before any identity is
  reported ready. If any check fails, every browser is terminated and the
  terminal says so.

Read [`docs/SCOPE.md`](docs/SCOPE.md) for the full requirements.

## Quick start

Requirements: Go 1.22+, and two upstream SOCKS5 or HTTP proxies you control
(one per identity) that egress from different public IPs.

```sh
git clone https://github.com/Nareik33L/tab-router && cd tab-router
go run ./scripts/fetch-chromium          # downloads + verifies the pinned Chromium
go build -o bin/tab-router ./cmd/tab-router
```

Create the route file with owner-only permissions:

* macOS: `~/Library/Application Support/tab-router/routes.toml` (`chmod 600`)
* Windows: `%LOCALAPPDATA%\tab-router\routes.toml`

```toml
[[route]]
id = "route-001"
type = "socks5"                       # socks5 | http
address = "proxy-a.example.net:1080"
username = "alice"
password_env = "TR_ROUTE_001_PASSWORD"   # or password = "..."

[[route]]
id = "route-002"
type = "http"
address = "proxy-b.example.net:3128"
```

Run:

```sh
bin/tab-router --identities 2 --url https://example.com
```

```
TAB ROUTER v0.1
Identities: 2   Startup URL: https://example.com
Using identity set set-2026-09-20T14-18-36Z
Browser: Chromium 153.0.8010.52 (pinned)
Starting 2 identities...
Identity 001 → Route 001: CONNECTED ✓
Identity 002 → Route 002: CONNECTED ✓
Verifying network isolation...
Identity 001 → Public IP: 203.0.113.10 ✓
Identity 002 → Public IP: 198.51.100.7 ✓
Identity 001 ≠ Identity 002 ≠ host ✓
Verifying DNS routing... ✓
Verifying IPv6 behaviour... ✓
Verifying browser storage isolation... ✓
Verifying fail-closed behaviour... ✓
Verifying no direct connections... ✓
Opening startup URL...
Identity 001 → https://example.com ✓
Identity 002 → https://example.com ✓
================================
TAB ROUTER READY
================================
Identity 001 → Route 001 → READY   203.0.113.10
Identity 002 → Route 002 → READY   198.51.100.7
Isolation verification: PASSED (9/9 checks per identity)
```

Other commands, from a second terminal:

```sh
bin/tab-router --status            # live state, health, environment per identity
bin/tab-router --diagnostics       # re-run the isolation checks and print a report
bin/tab-router --stop
bin/tab-router --fresh             # start a brand-new identity set (old one is kept)
bin/tab-router route-check         # dev: public IP of each configured route
```

Exit codes: `0` ok · `1` usage/config · `2` verification failed · `3` route
failed · `4` browser failed · `5` already running.

## Persistent identity environments

When an identity is first created its environment is chosen and written to
`identity.json`; from then on it is applied identically on every launch and
never changes on its own:

| Setting | How it is applied |
| --- | --- |
| Locale / `Accept-Language` | `--lang`, `intl.accept_languages`, spellcheck dictionary |
| Timezone | `TZ` for the process plus a DevTools timezone override on every page |
| Window size and position | `--window-size`, `--window-position` (identities are tiled, never stacked exactly) |
| Device scale factor, colour profile | `--force-device-scale-factor`, `--force-color-profile=srgb` |
| Colour scheme | `--force-dark-mode` when `dark` |
| Downloads | `download.default_directory` inside the identity directory |

Defaults are detected from the host once (locale, timezone) and pinned. To
choose them explicitly, add to `config.toml` **before** the identity is
created:

```toml
[environment]              # defaults for new identities
locale = "en-GB"
timezone = "Europe/London"
window = "1280x860"

[[identity]]               # per-identity override
index = 2
locale = "de-DE"
timezone = "Europe/Berlin"
```

Nothing here spoofs or randomises fingerprints; only settings Chromium
legitimately exposes are used, and the point is stability, not disguise.

## Configuration

`config.toml` lives next to `routes.toml`. All keys are optional:

```toml
identities = 2
startup_url = "https://example.com"
persistent_identities = true
fail_closed = true            # false is rejected in v0.1
ipv6 = true

[verify]
ip_echo_url = "https://api.ipify.org?format=json"
ipv6_echo_url = "https://api6.ipify.org?format=json"
compare_host_ip = true
timeout_seconds = 20

[health]
probe_interval_seconds = 10
ip_check_interval_seconds = 60
```

Data directory: `~/Library/Application Support/tab-router` (macOS),
`%LOCALAPPDATA%\tab-router` (Windows); override with `--data-dir` or
`TAB_ROUTER_DATA_DIR`. Chromium: the pinned build under
`<data-dir>/chromium/<version>/`, or `TAB_ROUTER_CHROMIUM=/path/to/binary`.

## Development

Linux is a development/CI host only; product platforms are Windows and macOS.

```sh
make vet          # go vet for linux, windows and darwin
make test-unit    # no browser needed
make infra        # local test network: IP echo + two proxies with distinct source IPs
make test-isolation   # needs TAB_ROUTER_CHROMIUM or `make fetch-chromium`
```

`tests/infra` emulates two upstream proxies that egress from `127.0.0.2`
and `127.0.0.3`, plus an IP-echo server, entirely in-process. The
isolation suite in `tests/isolation` drives the real startup sequence
headlessly against it. See [`docs/leak-testing.md`](docs/leak-testing.md)
for the manual procedure with a packet capture.

Layout:

```
cmd/tab-router        CLI
controller/startup    ordered startup sequence, terminal contract, session
controller/verify     checks V1–V9
controller/health     runtime probes, fail-closed enforcement, leak alarms
controller/browser    Chromium finder, launcher, CDP-over-pipe client, prefs
controller/identity   identity sets, identity.json, environments
controller/config     config.toml / routes.toml
routing/provider      Route, RoutingProvider, Gate
routing/socks5        SOCKS5 client and CONNECT-only server
routing/httpproxy     HTTP CONNECT client and server
routing/platform      OS-specific: process tree, endpoint enumeration, IPC, file ACLs
ipc                   authenticated local control channel
browser/chromium      pinned build manifest (embedded)
scripts/fetch-chromium  download + SHA-256 verify + extract
tests/                infra, unit, browser and isolation suites
docs/                 scope, engineering plan, ADRs, flag rationale, leak testing
```

## Security notes

* Route credentials live only in `routes.toml` (refused if readable by other
  users) or environment variables, and never reach Chromium or the terminal.
* The DevTools channel is a pipe, never a TCP port.
* The control channel is a Unix socket / named pipe with a per-session
  random token.
* This tool does not disguise the browser. It keeps identities separate and
  stable; it makes no anti-detection claims.

License: MIT.
