# Tab Router

Two isolated Chromium browsers on one Mac or Windows PC. Each has its own
cookies, window, and public IP. You do **not** need Go, a Tab Router account,
a `routes.toml` file, or any login.

Status: two identities, automatic routes. Cap stays at 2 until the isolation
suite is signed off ([`docs/ENGINEERING_PLAN.md`](docs/ENGINEERING_PLAN.md), M9).
Downloads: [latest GitHub Release](https://github.com/Nareik33L/tab-router/releases/latest).

## Quick start (Terminal, no Go)

You need a Mac or a Windows PC, and **Terminal** (Mac) or **PowerShell**
(Windows). There is nothing to sign up for.

The first start downloads official Chromium (not Google Chrome) and a
local network-exit helper (Tor). The windows are titled Chromium.
Tab Router then asks how many identities and which URL, provisions one
isolated route per identity, verifies them, and opens the browsers.

### Mac (Apple chip — M1, M2, M3, M4)

Most Macs from 2020 onward. Check: Apple menu (top left) → **About This Mac**.
If you see **Chip**, use this. If you see **Processor**, use the Intel block
below.

Open **Terminal** (Command-Space, type `Terminal`, press Enter). Paste the
whole block, then press Enter:

```sh
mkdir -p ~/tab-router-app
cd ~/tab-router-app
curl -L -o tab-router https://github.com/Nareik33L/tab-router/releases/latest/download/tab-router-mac-apple-silicon
chmod +x tab-router
xattr -c tab-router 2>/dev/null
./tab-router
```

When it asks:

```
Number of identities [2]:
Startup URL (blank for none):
```

Type `2`, press Enter, then paste a URL such as `https://example.com` and
press Enter. Or skip the prompts:

```sh
cd ~/tab-router-app
./tab-router --identities 2 --url https://example.com
```

Two browser windows should open. You never create a route file and you
never log in.

If macOS says the app is damaged or cannot be opened, run
`xattr -c ~/tab-router-app/tab-router` then retry `./tab-router` from
`~/tab-router-app`. If Terminal says there is no quarantine attribute,
ignore that and start the program.

Tab Router never needs your Mac password or administrator rights. If a
Keychain or password dialog appears, click Cancel — it is Chromium
asking, not Tab Router, and current builds suppress it.

### Mac (Intel)

```sh
mkdir -p ~/tab-router-app
cd ~/tab-router-app
curl -L -o tab-router https://github.com/Nareik33L/tab-router/releases/latest/download/tab-router-mac-intel
chmod +x tab-router
xattr -c tab-router 2>/dev/null
./tab-router --identities 2 --url https://example.com
```

### Windows

Open **PowerShell**. Paste:

```powershell
New-Item -ItemType Directory -Force -Path $HOME\tab-router-app | Out-Null
cd $HOME\tab-router-app
Invoke-WebRequest -Uri https://github.com/Nareik33L/tab-router/releases/latest/download/tab-router-windows.exe -OutFile tab-router.exe
.\tab-router.exe --identities 2 --url https://example.com
```

If Windows SmartScreen appears, choose **More info** → **Run anyway**.

### After it is running

From a **second** Terminal / PowerShell window:

```sh
cd ~/tab-router-app
./tab-router --status
./tab-router --stop
```

On Windows, first `cd $HOME\tab-router-app`, then use `.\tab-router.exe`
instead of `./tab-router`.

`--fresh` makes a new browser identity set. It does not change how routes
are provisioned.

You should see something like:

```
TAB ROUTER READY
Identity 001 → Route 001 → READY   …
Identity 002 → Route 002 → READY   …
Isolation verification: PASSED
```

## How it works

```
 tab-router (controller)
 ├─ Identity 001 ── Chromium #1 ──proxy──▶ Gate 001 ──▶ Route 001 (local Tor circuit) ──▶ egress IP-A
 └─ Identity 002 ── Chromium #2 ──proxy──▶ Gate 002 ──▶ Route 002 (local Tor circuit) ──▶ egress IP-B
```

* One Chromium process tree per identity, launched with a hardened flag set
  and `--proxy-server` pointing at a **gate** the controller owns.
* The gate is a local SOCKS5 server that forwards only while its route is
  verified `READY`. When the route fails the gate refuses every request, so
  the browser shows a connection error instead of using the host network.
  Chromium never sees Tor, relays, or credentials.
* On start, Tab Router downloads a pinned Tor Expert Bundle if needed and
  provisions one independent circuit per identity (no administrator rights,
  no host routing changes, no account). The public IP observed at startup
  is pinned for the rest of that session and is not allowed to rotate.
  Traffic leaves through Tor; some sites block it. That is the no-login
  way to get two different public IPs.
* DNS is delegated through the route; the browser never resolves hostnames
  locally.
* At startup nine checks (V1–V9) prove the isolation before any identity is
  reported ready. If any check fails, every browser is terminated and the
  terminal says so.

Read [`docs/SCOPE.md`](docs/SCOPE.md) for the full requirements and
[`docs/adr/0004-local-automatic-exits.md`](docs/adr/0004-local-automatic-exits.md)
for why the default path has no login.

Extra commands (from `~/tab-router-app`, or `$HOME\tab-router-app` on Windows):

```sh
./tab-router --diagnostics
./tab-router --fresh
```

A `routes.toml` of your own SOCKS/HTTP proxies is optional. See
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

`config.toml` lives in the data directory. All keys are optional:

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
`TAB_ROUTER_DATA_DIR`. Chromium: the official snapshot pinned in
`browser/chromium/pin.json`, stored under `<data-dir>/chromium/<revision>/`,
or `TAB_ROUTER_CHROMIUM=/path/to/binary`.

## Development

Linux is a development/CI host only; product platforms are Windows and macOS.
Building from source needs Go 1.22+. You do not need this to use Tab Router.

```sh
git clone https://github.com/Nareik33L/tab-router && cd tab-router
go run ./scripts/fetch-chromium
go build -o bin/tab-router ./cmd/tab-router
bin/tab-router --identities 2 --url https://example.com

make vet
make test-unit
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
routing/manager       Provisioner (local Tor by default, static/Mullvad overrides)
routing/provider      Route, RoutingProvider, Gate
routing/tor           pinned Tor Expert Bundle + local daemon
routing/wireguard     userspace WireGuard (optional Mullvad override)
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

* There is no Tab Router account. Normal startup stores no cloud credentials.
  Optional `routes.toml` (bring-your-own upstreams) is owner-only and is
  redacted from logs.
* The DevTools channel is a pipe, never a TCP port.
* The control channel is a Unix socket / named pipe with a per-session
  random token.
* This tool does not disguise the browser. It keeps identities separate and
  stable; it makes no anti-detection claims.

License: MIT.
