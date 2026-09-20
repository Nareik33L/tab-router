# Tab Router

Terminal-driven, Chromium-based browser for Windows and macOS that runs
several **persistent, isolated browsing identities** at once. Each identity
has its own browser profile, its own device environment (locale, timezone,
window, downloads) and its own network route with its own public IP.
Traffic from one identity can never fall back to another route or to the
host's normal connection: if a route is down, that identity is blocked.

Status: **v0.1.0 — two-identity milestone.** The identity count is capped at
2 until the isolation suite has been signed off (see
[`docs/ENGINEERING_PLAN.md`](docs/ENGINEERING_PLAN.md), M9).

## Install from a GitHub Release

Download the binary for your platform from
[Releases](https://github.com/Nareik33L/tab-router/releases):

| File | Platform |
| --- | --- |
| `tab-router-vX.Y.Z-windows-amd64.exe` | Windows 10/11 (x64) |
| `tab-router-vX.Y.Z-darwin-arm64` | macOS Apple Silicon |
| `tab-router-vX.Y.Z-darwin-amd64` | macOS Intel |

On first run the binary downloads the pinned Chromium into the data
directory. One-time network setup is `tab-router provider login` (a Mullvad
account, because two identities need two real public egress IPs). Unsigned
builds: on macOS run `xattr -d com.apple.quarantine tab-router`; on Windows,
allow the SmartScreen prompt.

## How it works

```
 tab-router (controller)
 ├─ Identity 001 ── Chromium #1 ──proxy──▶ Gate 001 ──▶ Route 001 (userspace WG) ──▶ egress IP-A
 └─ Identity 002 ── Chromium #2 ──proxy──▶ Gate 002 ──▶ Route 002 (userspace WG) ──▶ egress IP-B
```

* One Chromium process tree per identity, launched with a hardened flag set
  and `--proxy-server` pointing at a **gate** the controller owns.
* The gate is a local SOCKS5 server that forwards only while its route is
  verified `READY`. When the route fails the gate refuses every request, so
  the browser shows a connection error instead of using the host network.
  Chromium never sees WireGuard, relays, or credentials.
* The route manager provisions one independent userspace WireGuard tunnel
  per identity (no administrator rights, no host routing changes), verifies
  connectivity and egress, and tears the tunnels down on exit.
* DNS is delegated through the route; the browser never resolves hostnames
  locally.
* At startup nine checks (V1–V9) prove the isolation before any identity is
  reported ready. If any check fails, every browser is terminated and the
  terminal says so.

Read [`docs/SCOPE.md`](docs/SCOPE.md) for the full requirements and
[`docs/adr/0003-automatic-route-provisioning.md`](docs/adr/0003-automatic-route-provisioning.md)
for why Mullvad + userspace WireGuard is the first provider.

## Quick start (from source)

Requirements: Go 1.22+ and a [Mullvad](https://mullvad.net) account.

```sh
git clone https://github.com/Nareik33L/tab-router && cd tab-router
go run ./scripts/fetch-chromium          # downloads + verifies the pinned Chromium
go build -o bin/tab-router ./cmd/tab-router

bin/tab-router provider login            # one-time; paste the account number
bin/tab-router --identities 2 --url https://example.com
```

Or, with a TTY and no flags, `bin/tab-router` prompts for the identity count
and URL. `routes.toml` is not required.

```
TAB ROUTER v0.1.0
Identities: 2   Startup URL: https://example.com
Using identity set set-2026-09-20T14-18-36Z
Creating Identity 001...
Creating Identity 002...
Browser: Chromium 153.0.8010.52 (pinned)
Network provider: mullvad
Provisioning network routes...
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
bin/tab-router --fresh             # new browser identity set; provider config is kept
bin/tab-router provider status
bin/tab-router provider logout     # removes local provider.toml only
bin/tab-router route-check         # dev: public IP of each provisioned route
```

Power users can still drop a `routes.toml` of SOCKS5/HTTP upstreams in the
data directory; it is used only when `provider.toml` is absent. See
[`docs/routes.example.toml`](docs/routes.example.toml).

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

`config.toml` lives in the data directory next to `provider.toml`. All keys
are optional:

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
controller/config     config.toml / optional routes.toml
routing/manager       Provisioner (Mullvad, static fallback)
routing/provider      Route, RoutingProvider, Gate
routing/wireguard     userspace WireGuard (wireguard-go + netstack)
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

* Provider credentials live only in `provider.toml` (refused if readable by
  other users) and never reach Chromium or the terminal. Optional
  `routes.toml` is the same for bring-your-own upstreams.
* The DevTools channel is a pipe, never a TCP port.
* The control channel is a Unix socket / named pipe with a per-session
  random token.
* This tool does not disguise the browser. It keeps identities separate and
  stable; it makes no anti-detection claims.

License: MIT.
