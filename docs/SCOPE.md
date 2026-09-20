# Tab Router — Technical Scope v0.4

Supersedes v0.3. Changes from v0.3 are marked **[v0.4]** and are summarised in
Appendix C. v0.3 changes remain in Appendix B.

---

## 1. Objective

Build an open-source, terminal-driven, Chromium-based browser application for
Windows and macOS that creates multiple persistent, isolated browsing
identities.

Each identity has:

* its own browser state (cookies, local storage, IndexedDB, cache,
  service-worker state, history, session state);
* its own network route;
* its own public egress IP where the configured route infrastructure provides
  one;
* isolation from every other identity.

On startup the user specifies (1) the number of identities and (2) an optional
URL to open in every identity.

**[v0.4] Route sourcing.** The application does not create public IP
addresses and does not have a Tab Router account. Tab Router owns the route
lifecycle: it provisions one independent egress path per identity, verifies
connectivity, binds the identity to that route, keeps the route up while the
session runs, and tears it down on exit. The default mechanism is a local
Tor daemon (pinned Expert Bundle, no user account, no login). Chromium never
sees Tor; it only sees its local Gate. A leftover `routes.toml` remains a
power-user override. "No manual networking configuration" means: the
application never changes host routing tables, adapters, DNS settings or
firewall rules, never requires administrator rights for normal use, and the
user never configures a browser tab, proxy host, SOCKS credential, or
provider login by hand.

**[v0.2] Two identities first.** The initial implementation targets exactly
two identities and two routes. The identity count is hard-capped at 2 until
the scaling milestone (M9). Nothing in the design may assume the cap is
permanent.

The initial product does not require a polished GUI.

---

## 2. Primary success criterion

```
Identity 001 → Route 001 → Public IP A
Identity 002 → Route 002 → Public IP B
```

running simultaneously on one computer, both loading the same URL, with A ≠ B,
neither equal to the host's own public IP, and demonstrable evidence that
Identity 001's traffic cannot reach Route 002 or the host's default
connection.

**[v0.2]** Launching two browsers is not success. Success is the application
*verifying* the above at startup and printing the verdict (see §21).

The same architecture must later support `Identity N → Route N` with no
per-tab manual configuration.

---

## 3. Platforms

Required: Windows 11+, macOS 13+ (Apple silicon and Intel).

Linux is a development/CI convenience target only and is not a supported
product platform. **[v0.2]**

Platform-specific code lives behind a common interface:

```
RoutingProvider / PlatformProvider
├── windows/   (named-pipe IPC, per-process connection table via iphlpapi, file ACLs)
└── macos/     (unix-socket IPC, per-process connection table via libproc/lsof, POSIX perms)
```

Everything else is platform-neutral.

---

## 4. Browser

Use Chromium.

* Ship a pinned official Chromium snapshot with the application,
  downloaded by revision and SHA-256 at first run. **[v0.2]**
* v0.1 uses a stock build with command-line flags only. No Chromium fork. The
  `browser/patches/` directory stays empty until a written ADR documents a need
  a flag cannot satisfy. **[v0.2]**
* Users must not need to install extensions into an existing browser.

**[v0.2] Process model.** Each identity is a *separate Chromium process tree*
with its own `--user-data-dir`. In v0.1 "Tab N" is therefore "window N (one
tab)". Rationale: CDP `Target.createBrowserContext` proxy-per-context
contexts are ephemeral and cannot satisfy persistence; a per-identity process
is a clean failure domain for fail-closed; no Chromium modification is needed.
Single-window multi-tab presentation is deferred (see §25).

---

## 5. Identity model

An identity is a persistent Chromium profile directory plus metadata:

```
<data-dir>/identities/
├── current -> set-2026-09-20T13-00-00Z        (pointer to active set)
├── set-2026-09-20T13-00-00Z/
│   ├── identity-001/
│   │   ├── profile/        (Chromium user-data-dir)
│   │   └── identity.json   (id, created, route slot, chromium version)
│   └── identity-002/
└── set-2026-09-01T09-12-44Z/                  (older set, retained)
```

Independent per identity: cookies, local storage, IndexedDB, cache,
service-worker state, history, session state.

Identity A must not be able to read Identity B's storage (verified by test D
and by startup check V6).

No fingerprint spoofing in v0.1. The goal is isolation, not artificial
difference.

---

## 6. Startup identity count

```
tab-router --identities 2
```

Interactive mode (no flags, TTY present):

```
TAB ROUTER v0.1
Number of identities [2]: 2
Startup URL (blank for none):
```

**[v0.2]** Default is 2. Values above the current cap (2 until M9) are rejected
with an explanatory message. The identity count may never exceed the number
of configured routes; routes are never shared between identities.

Mapping is deterministic: `Identity 00N → Route 00N → Window N`.

---

## 7. Startup URL

```
tab-router --identities 2 --url https://example.com
```

Requirements:

* Validate as an absolute URL with scheme `http` or `https`; reject anything
  else with exit code 1.
* Do not modify, normalise beyond RFC 3986 parsing, or redirect the URL.
* Pass the URL independently to each identity via CDP `Page.navigate` (not on
  the Chromium command line, so it does not appear in process listings).
  **[v0.2]**
* If omitted, open a blank page.
* **[v0.2]** The URL is opened only *after* the identity has passed startup
  verification (§21). Redirects issued by the site are followed normally and
  the final URL is recorded in status output.

---

## 8. Network model

```
Identity 001 → Route 001
Identity 002 → Route 002
```

**[v0.2] Route Gate.** Each identity's Chromium is configured with a single
fixed proxy: a controller-owned SOCKS5 listener on `127.0.0.1:<random port>`
("the gate"), with no DIRECT fallback and loopback bypass disabled. The gate
forwards connections to that route's upstream through a `RoutingProvider`.
Consequences:

* Chromium never holds upstream addresses or credentials.
* Fail-closed is structural: route down ⇒ gate refuses ⇒ browser gets a
  network error. No OS firewall is required for the primary guarantee.
* Hostnames reach the gate unresolved (SOCKS5 DOMAINNAME), so the gate can
  observe and enforce DNS delegation.

Route providers:

| Provider | v0.1 status | DNS resolution happens at |
|---|---|---|
| Mullvad via userspace WireGuard (wireguard-go + netstack, no OS interface, no admin) | **required (automatic)** | route-configured DNS server, queried through the tunnel |
| SOCKS5 upstream (with optional user/pass auth handled by the gate) | power-user / tests | upstream proxy |
| HTTP CONNECT upstream (with optional Basic auth handled by the gate) | power-user / tests | upstream proxy |
| Future providers (Tor, SSH, other commercial VPN APIs) | out of scope | — |

`RoutingProvider` interface (illustrative):

```
CreateRoute(def) -> Route
Route.Start(ctx) error
Route.Stop() error
Route.Status() RouteStatus          // CREATED|STARTING|VERIFYING|READY|DOWN|STOPPED
Route.Dial(ctx, network, hostport) (net.Conn, error)   // used by the gate
Route.PublicIP(ctx) (v4, v6 *net.IP, err)              // controller-side probe
```

The browser layer knows only the gate address.

---

## 9. Critical networking requirement

Traffic belonging to an identity must remain associated with its route.

Test at minimum: HTTPS, HTTP, DNS, IPv4, IPv6, WebSockets, redirects,
downloads, service workers.

Investigate and document separately before making any claim: HTTP/3/QUIC,
WebRTC/UDP.

**[v0.2] Chromium hardening flags** (final list maintained in
`docs/chromium-flags.md`, validated against the pinned build):

* `--proxy-server=socks5://127.0.0.1:<gate>` and `--proxy-bypass-list=<-loopback>`
* `--host-resolver-rules="MAP * ~NOTFOUND , EXCLUDE 127.0.0.1"` (no local DNS)
* Secure DNS / DoH and async DNS disabled
* `--disable-quic`
* `--force-webrtc-ip-handling-policy=disable_non_proxied_udp`
* `--remote-debugging-pipe` (never `--remote-debugging-port`)
* Background networking, component updates, sync, first-run, default-browser
  check, safe-browsing auto-update disabled

Chromium has no SOCKS5 UDP support; with the flags above no UDP should leave
the browser. That statement must be *tested* (T-H, T-WebRTC), not assumed.

---

## 10. Fail-closed behaviour

If Route 001 is unavailable, Identity 001 is blocked. It must never use the
host's connection or another identity's route.

Mechanisms, in layers:

1. Structural: gate is the only configured proxy; gate refuses when route is
   not READY and closes existing tunnels on transition to DOWN.
2. Observational: the platform provider enumerates the identity's Chromium
   process tree's remote endpoints; anything other than the gate is a
   verification failure and a health alarm.
3. OS enforcement (stretch, S2): per-process firewall rule (Windows WFP;
   macOS Network Extension) as defence in depth.

Test procedure: start → verify → disconnect route → request → assert blocked
→ restore → assert resumed with the same public IP.

---

## 11. DNS

DNS must be resolved by the route, never by the host resolver.

* Chromium local resolution disabled (§9).
* Gate records the SOCKS5 address type of every CONNECT; a hostname
  navigation that arrives as an IP literal indicates local resolution and is
  a verification failure.
* Test infrastructure includes a DNS oracle: an authoritative name server for
  a test zone that records the resolver IP for unique per-test subdomains
  (T-B).
* Test IPv6 DNS, system-resolver fallback, and route-failure fallback.

---

## 12. Persistent identities

Identities persist between launches. Default launch reuses the `current` set.

```
tab-router --fresh --identities 2
```

creates a new timestamped set and repoints `current`; the old set is retained
(never deleted or overwritten silently). Identity → route slot pinning is
stored in `identity.json`, so identity 001 always uses route slot 1. The
route itself is provisioned fresh each start; the identity's browser state
is what persists.

A lock file per set prevents two controllers from opening the same identities.

---

## 13. Identity-to-tab mapping

Deterministic `Identity 00N → Window N`. No tab switching or reassignment in
v0.1. The user may open extra tabs inside a window; they inherit that
identity and route because they live in the same profile/process.

---

## 14. Controller

```
CLI
 │  (IPC: unix socket / named pipe, token-authenticated)
 ▼
Controller (daemon for the session)
 ├── IdentityManager    create/load/lock identity sets
 ├── RouteManager       RoutingProvider instances, Gate per route, state machine
 ├── BrowserManager     Chromium launch, CDP over pipe, process-tree tracking
 ├── StartupManager     ordered startup (§15), terminal reporting
 ├── Verifier  [v0.2]   startup checks V1–V9, diagnostics checks
 ├── HealthMonitor      periodic route probes, DOWN/READY transitions, leak alarms
 └── PlatformProvider   OS-specific: connection tables, IPC endpoints, permissions
```

---

## 15. Startup sequence

```
 1. Parse CLI, load config.toml (CLI overrides file)
 2. Determine identity count (≤ cap)
 3. Validate startup URL
 4. Create/load identity set; acquire lock
 5. Resolve provisioner (default local Tor; optional leftover provider.toml or routes.toml)
 6. Provision N independent routes and create gates (gates start CLOSED)
 7. Start routes; controller-side verification (V1): public IP per route
 8. Launch one Chromium per identity, pinned to its gate; open gates
 9. Browser-side verification (V2–V8) per identity, cross-identity checks
10. On any failure: kill all Chromium, tear routes down, print FAILED, exit 2
11. Open startup URL in each identity (V9)
12. Print READY summary; start HealthMonitor (fail-closed re-establish); serve IPC
```

If a route cannot be established, its identity is not launched.

---

## 16. Local IPC

* macOS: Unix domain socket in the app's run directory, mode 0600.
* Windows: named pipe `\\.\pipe\tab-router-<session>` with a DACL restricted
  to the current user.
* Localhost TCP only as an explicitly configured fallback, still
  token-authenticated.
* Per-session random token in a 0600 file; every request carries it.
* Line-delimited JSON request/response.

Methods: `status`, `identities.list`, `routes.list`, `route.start`,
`route.stop`, `identity.open_url`, `diagnostics.run`, `shutdown`.

Browser control uses CDP over `--remote-debugging-pipe`, inherited only by the
controller. No unauthenticated DevTools TCP port.

---

## 17. Security

* No telemetry, Tab Router accounts, or transmission of browsing data.
* Normal startup does not create or require a cloud credential file.
  Optional `routes.toml` (bring-your-own upstreams) uses owner-only
  permissions and is redacted from all logs and status output.
* The startup URL is not placed on any process command line.
* Chromium runs with its default sandbox; never pass `--no-sandbox`.
* One outbound request from the *controller* over the host connection is
  permitted for the "host IP ≠ identity IP" comparison; it is configurable
  (`verify.compare_host_ip`) and printed when it happens. No browser traffic
  ever uses the host connection.

---

## 18. CLI

```
tab-router                                   interactive if TTY, else defaults
tab-router --identities 2
tab-router --identities 2 --url https://example.com
tab-router --fresh --identities 2 --url https://example.com
tab-router --status
tab-router --stop
tab-router --diagnostics [--full]
tab-router --config <path> --routes <path> --data-dir <path>
tab-router --version
```

Exit codes: 0 success; 1 usage/config error; 2 verification failed (shut down
safely); 3 route establishment failed; 4 browser launch failed; 5 already
running.

Terminal output contract: see §21.

---

## 19. Configuration

`config.toml` (non-secret):

```toml
identities = 2
startup_url = "https://example.com"
persistent_identities = true
fail_closed = true            # informational; cannot be set to false in v0.1
ipv6 = true

[verify]
ip_echo_url = "https://api.ipify.org?format=json"
ipv6_echo_url = "https://api6.ipify.org?format=json"
compare_host_ip = true

[health]
probe_interval_seconds = 10
```

Routes are not persisted across sessions; identities are. There is no
`provider.toml` on the normal path.

`routes.toml` is an optional power-user override:

```toml
[[route]]
id = "route-001"
type = "socks5"
address = "proxy-a.example.net:1080"
username = "alice"
password_env = "TR_ROUTE_001_PASSWORD"   # or password = "..."

[[route]]
id = "route-002"
type = "http"
address = "proxy-b.example.net:3128"
```

A `routes.example.toml` with placeholders ships in the repo. CLI flags
override `config.toml`. Paths: Windows `%LOCALAPPDATA%\tab-router\`; macOS
`~/Library/Application Support/tab-router/`.

---

## 20. Diagnostics

`tab-router --diagnostics` runs the startup verification checks against a
running instance (or starts a temporary one) and prints a per-identity report:

```
Browser        OK   Chromium 128.0.6613.84 (pinned)
Controller     OK   pid 4242, IPC unix:…/control.sock
Identity 001 → Route 001
  Storage        OK   profile isolated, distinct realpath
  Route          OK   socks5 upstream reachable
  Public IPv4    OK   203.0.113.10
  Public IPv6    OK   blocked (route has no IPv6 egress)
  DNS            OK   delegated to route (0 local lookups)
  Fail-closed    OK   blocked while route down, resumed after restore
  No direct      OK   0 non-gate endpoints observed
  Startup URL    OK   https://example.com (200)
Identity 002 → Route 002
  ...
```

`--full` additionally runs WebSocket, redirect, download, service-worker,
WebRTC and QUIC probes (needs the test-infra echo service or public
equivalents). Diagnostics deliberately induce failure (route down) and confirm
blocking.

---

## 21. Startup verification and terminal output **[v0.2]**

Verification is mandatory and cannot be disabled in release builds. Starting
Chromium and opening tabs is not success.

Per identity (V1–V9) and cross-identity (V3):

| ID | Check | Method | Pass |
|---|---|---|---|
| V1 | Route up | Controller fetches `ip_echo_url` through the route | HTTP 200, IP parsed |
| V2 | Browser egress | CDP navigates a hidden verification tab to `ip_echo_url`, reads body | IP == V1 IP |
| V3 | Separation | Compare all V2 IPs and host IP | pairwise distinct; none == host |
| V4 | DNS delegation | Gate saw CONNECT for echo hostname with DOMAINNAME; zero IP-literal CONNECTs for hostname navigations | true |
| V5 | IPv6 | Navigate to `ipv6_echo_url` | route IPv6 (≠ host IPv6) **or** blocked; never host IPv6 |
| V6 | Storage isolation | Set cookie + localStorage in 001 on echo origin; read in 002; compare profile realpaths | absent in 002; paths distinct |
| V7 | Fail-closed | Gate forced DOWN; navigate; then restore; navigate | error while down; success through the same gate after restore (Tor exit may rotate; must not become the host IP) |
| V8 | No direct connections | Platform provider enumerates remote endpoints of the identity's process tree throughout V2–V7 | only `127.0.0.1:<gate>` |
| V9 | Startup URL | After open, CDP main-frame document response **or** a successful gate CONNECT to the URL host (do not wait for a full loadEvent — ads/trackers over Tor often never finish) | true (M4+) |

Success output (engineer may restyle; content is mandatory):

```
TAB ROUTER v0.1
Identities: 2   Startup URL: https://example.com
Starting 2 identities...
Identity 001 → Route 001 → CONNECTED
Identity 002 → Route 002 → CONNECTED
Verifying network isolation...
Identity 001 → Public IP: 203.0.113.10 ✓
Identity 002 → Public IP: 198.51.100.7 ✓
Identity 001 ≠ Identity 002 ≠ host ✓
Verifying DNS routing... ✓
Verifying IPv6 behaviour... ✓ (001 blocked, 002 blocked)
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

Failure output:

```
Identity 002 → Route 002 → CONNECTED
Verifying network isolation...
Identity 001 → Public IP: 203.0.113.10 ✓
Identity 002 → Public IP: FAILED (V2: browser egress 192.0.2.44 != route 198.51.100.7)
ERROR: Identity 002 could not be verified as isolated.
The identity will NOT use the host/default network.
Shutting down all identities...
================================
TAB ROUTER FAILED TO START SAFELY
================================
exit code 2
```

Rules: an identity that fails any check is never marked READY and never
receives the startup URL; any failure aborts the whole session (v0.1 has no
partial mode); no silent fallback of any kind.

---

## 22. Automated tests

Isolation suite (`tests/isolation/`), each mapped to a verification check:

* **T-A IP separation** — IP A ≠ IP B ≠ host (V2, V3)
* **T-B DNS separation** — unique-subdomain lookups appear at the DNS oracle
  only from the route's resolver; none from the host resolver (V4)
* **T-C Route failure** — kill Route A → request blocked → restore → resumes (V7)
* **T-D Cross-identity storage** — cookie/localStorage/IndexedDB written in A
  absent in B (V6)
* **T-E Restart persistence** — state in A survives controller restart; `--fresh`
  leaves old set intact
* **T-F Startup URL** — N windows, N identities, N requests, each via its route (V9)
* **T-G Concurrency** — 2 identities (M3), then 5 and 10 (M9): CPU, RAM,
  connection count, startup time recorded
* **T-H No direct connections** — process-tree connection table and packet
  capture show only gate traffic (V8)
* **T-I Verification reporting** — induced failures produce FAILED output,
  exit 2, no surviving Chromium processes
* **T-J Protocol coverage** — WebSocket, redirects (301/302/307/308,
  cross-origin), downloads, service-worker fetches, all via route
* **T-K Investigations** — WebRTC/UDP and HTTP/3/QUIC; result documented in
  `docs/leak-testing.md` before any isolation claim

Test infrastructure (`tests/infra/`, Docker Compose): IP-echo HTTP(S) server,
DNS oracle, two SOCKS5 proxies and one HTTP proxy with distinct IPs on a test
network. Runs on Linux CI for logic; Windows and macOS CI runners execute the
platform suites.

---

## 23. Repository structure

```
tab-router/
├── cmd/tab-router/          CLI entry point
├── controller/
│   ├── identity/  browser/  routing/  startup/  verify/  health/
├── routing/
│   ├── provider/            RoutingProvider interface, gate
│   ├── socks5/  http/       upstream providers
│   ├── wireguard/           stretch
│   ├── windows/  macos/     PlatformProvider implementations
├── ipc/
├── browser/
│   ├── chromium/            pin manifest (version, SHA-256 per platform)
│   └── patches/             empty in v0.1
├── tests/
│   ├── infra/  browser/  routing/  isolation/  integration/
├── scripts/                 fetch-chromium, package, release
├── docs/                    SCOPE.md, ENGINEERING_PLAN.md, adr/, chromium-flags.md, leak-testing.md
├── README.md                brief; startup commands only
└── LICENSE
```

---

## 24. README requirement

Unchanged: the README contains only how to start the application (start,
fresh identities, status, diagnostics). Everything else lives in `docs/`.

---

## 25. Development milestones

See `docs/ENGINEERING_PLAN.md` for exit criteria. Summary:

| # | Milestone | Identities |
|---|---|---|
| M0 | Bootstrap: repo, CI, Chromium pin, test infra | — |
| M1 | Two persistent identities (normal networking; last milestone allowed DIRECT) | 2 |
| M2 | Two routes → two public IPs, controller-side, no browser | 2 |
| M3 | **Bind identities to routes + startup verification + fail-closed** (primary) | 2 |
| M4 | Startup URL | 2 |
| M5 | Leak test suite and investigations | 2 |
| M6 | Orchestration: daemon, IPC, status/stop/diagnostics, health, config, `--fresh` | 2 |
| M7/M8 | Windows and macOS platform providers; full suite on each | 2 |
| M9 | Lift cap; 5 and 10 identities; measure | 5, 10 |
| M10 | Packaging: Windows installer, macOS .dmg, reproducible builds | — |
| S1 | Automatic route provisioning (userspace WireGuard + Mullvad) | 2 |
| S2 | Stretch: OS-level per-process firewall as second fail-closed layer | — |

---

## 26. Explicitly out of scope for v0.1

Fancy GUI; user accounts; cloud backend; analytics; subscriptions; mobile;
extension for existing Chrome; fingerprint spoofing; anti-detection; CAPTCHA
solving; automated account creation; scraping; platform-specific evasion.

**[v0.3] also:** single-window multi-tab presentation; tab reassignment
between identities; sharing a route between identities; Chromium source
modifications; Linux as a supported platform; running without verification.

---

## 27. Definition of done

v0.1 is done when a clean Windows 11 machine and a clean macOS 13+ machine
can each run

```
tab-router --identities 2 --url https://example.com
```

with no account, no login, and no `routes.toml`, and print
`TAB ROUTER READY … Isolation verification: PASSED`, with:

* independent browser storage (T-D)
* simultaneous operation
* DNS isolation (T-B)
* IPv4/IPv6 protection (T-A, V5)
* no silent fallback, verified by observation (T-H)
* route failure blocks the identity (T-C)
* persistent identities across restart (T-E)
* startup URL opened in each identity through its route (T-F)
* induced failures reported as FAILED with exit 2 (T-I)
* WebSocket/redirect/download/service-worker coverage (T-J); WebRTC and
  HTTP/3 findings documented (T-K)
* automated tests in CI; reproducible builds; public GitHub source; minimal
  README.

Only then is the cap lifted to 5, 10 and beyond (M9).

---

## 28. Engineering principle

Do not optimise for appearance. Optimise for proving isolation, and tell the
user the result in the terminal.

```
WINDOW → IDENTITY → CHROMIUM PROCESS → GATE → ROUTE → PUBLIC EGRESS
```

---

## Appendix A — Summary of v0.2 changes

1. Exactly two identities first; hard cap until M9.
2. Mandatory startup verification (V1–V9) with a terminal output contract;
   failure ⇒ identity never READY ⇒ session aborts with exit 2.
3. Route sourcing made explicit: user-supplied upstreams in `routes.toml`;
   "no manual networking" defined as no OS network changes and no admin.
4. One Chromium process per identity; "tab" = window in v0.1; rationale given.
5. Route Gate design: local per-identity SOCKS5 listener owned by the
   controller; structural fail-closed; credentials never reach Chromium.
6. Concrete DNS strategy: Chromium local resolution disabled; gate observes
   SOCKS5 address type; DNS oracle in test infra.
7. UDP/QUIC/WebRTC hardening flags specified; still must be tested.
8. CDP over `--remote-debugging-pipe`; startup URL never on the command line.
9. Pinned, checksummed Chromium download; no fork; `patches/` empty.
10. Platform provider surface defined (connection tables, IPC, permissions).
11. Route state machine, identity-set layout with retained old sets, lock
    files, exit codes, IPC method list, config/route file formats.
12. Test suite extended with T-H (no direct connections), T-I (failure
    reporting), T-J (protocol coverage), T-K (investigations).
13. Linux declared dev/CI-only; WireGuard and OS firewall declared stretch.

## Appendix B — Summary of v0.3 changes

1. Automatic route provisioning: Identity N → Route N → Egress N. First
   milestone remains two identities / two routes.
2. First provider: Mullvad via userspace WireGuard. One-time
   `tab-router provider login`. No `routes.toml` for normal startup.
3. Routes are session-scoped; identities persist. `--fresh` does not
   destroy provider configuration.
4. Fail-closed re-establish of a failed route onto a different path;
   never a silent fallback to the host or another identity.
5. Verification (V1–V9) remains mandatory after automatic creation.

## Appendix C — Summary of v0.4 changes

1. Normal startup has no Tab Router account and no `provider login`.
2. Default exits are provisioned locally via a pinned Tor Expert Bundle
   (no cloud credentials). Distinct circuits per identity; V1–V9 still prove
   distinct public IPs.
3. `routes.toml` remains an optional override. A leftover Mullvad
   `provider.toml` is optional and is never created by normal startup.
