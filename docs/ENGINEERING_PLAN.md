# Tab Router — Engineering Plan (v0.2)

Companion to `docs/SCOPE.md`. This is the build order. Where the two disagree,
the scope wins; open an ADR in `docs/adr/` if you need to deviate.

## 0. Read this first

You are building a privacy tool whose entire value is a provable statement:

```
Identity 001 → Route 001 → Public IP A
Identity 002 → Route 002 → Public IP B        A ≠ B ≠ host, no fallback, ever
```

Four rules that override everything else:

1. **Two identities first.** Hard-cap `--identities` at 2 until milestone M9.
   Design for N; ship for 2.
2. **Prove it, then print it.** The app must verify isolation at startup and
   report the verdict in the terminal. Two open browser windows are not
   success. A failed check means that identity is never `READY`, the session
   aborts, and the exit code is 2.
3. **Fail closed by construction.** Chromium is only ever allowed to talk to a
   local gate owned by the controller. If the route is down, the gate refuses.
   There is no code path that yields DIRECT.
4. **No manual networking for the user.** The app never touches host routes,
   adapters, DNS or firewall, and never needs admin. The user supplies upstream
   route endpoints in `routes.toml` once; that is the only network input.

Do not spend time on: GUI polish, single-window tabs, fingerprinting,
Chromium forks, cloud anything. Optimise for proving isolation.

## 1. Locked architecture decisions

Each of these needs an ADR to change.

**D1 — One Chromium process tree per identity.** Own `--user-data-dir`, own
gate. "Tab N" = window N in v0.1. Why: CDP per-context proxies
(`Target.createBrowserContext`) are ephemeral and cannot be persistent;
per-process is a clean fail-closed domain; no Chromium patch needed.

**D2 — Route Gate.** Per identity, the controller runs a SOCKS5 listener on
`127.0.0.1:<random port>` and launches Chromium with that as its *single* fixed
proxy (`--proxy-server=socks5://127.0.0.1:PORT --proxy-bypass-list=<-loopback>`).
The gate dials upstream via the route's `RoutingProvider`. Gate states:
`CLOSED` (refuse all) / `OPEN` (forward). Credentials live in the gate, never in
Chromium. On route `DOWN` the gate goes `CLOSED` and tears down live tunnels.

**D3 — Route providers for v0.1:** upstream SOCKS5 (user/pass handled by the
gate) and upstream HTTP CONNECT (Basic auth handled by the gate). WireGuard via
userspace netstack (no OS interface, no admin) is stretch S1. Routes are never
shared between identities.

**D4 — Language: Go (recommended).** Single static binaries for
win/amd64, darwin/arm64, darwin/amd64; mature SOCKS5/HTTP proxy libraries;
CDP clients; `golang.zx2c4.com/wireguard/tun/netstack` for S1; easy
per-process connection-table calls via `golang.org/x/sys`. If you choose Rust
or another language, write ADR-0001 explaining how you cover the same ground.

**D5 — Chromium is pinned and downloaded, not forked.** `browser/chromium/pin.json`
holds version + SHA-256 per platform (Chromium snapshot or Chrome for Testing).
`scripts/fetch-chromium` verifies the checksum. `browser/patches/` stays empty
until an ADR documents a need no flag can meet.

**D6 — CDP over `--remote-debugging-pipe`.** Never `--remote-debugging-port`.
If your CDP library cannot speak the pipe transport, wrap it (fds 3/4 in, out)
or pick another library; a localhost DevTools TCP port is not acceptable in
release builds.

**D7 — DNS never resolves on the host.** Chromium flags:
`--host-resolver-rules="MAP * ~NOTFOUND , EXCLUDE 127.0.0.1"`, secure DNS/DoH
off, async DNS off. Hostnames reach the gate as SOCKS5 `DOMAINNAME`; the gate
logs the address type of every CONNECT. Resolution happens at the upstream
proxy (or inside the tunnel for S1).

**D8 — No UDP from the browser.** `--disable-quic`,
`--force-webrtc-ip-handling-policy=disable_non_proxied_udp`. Chromium has no
SOCKS5 UDP support, so nothing should leave over UDP. Test it (T-H, T-K);
do not assume it.

**D9 — Deterministic, persisted mapping.** `identity-00N ↔ route slot N ↔ window N`,
recorded in `identity.json`, stable across restarts.

**D10 — Verification is mandatory.** Checks V1–V9 (below) run on every start.
No release-build flag disables them. A dev-only `--unsafe-skip-verification`
may exist behind a build tag that release CI does not set.

**D11 — Platform code is quarantined** in `routing/windows` and
`routing/macos` behind `PlatformProvider`: per-process remote-endpoint
enumeration, IPC endpoint creation, owner-only file permissions. Linux builds
are for dev/CI only.

## 2. Repository layout (Go module `github.com/<org>/tab-router`)

```
cmd/tab-router/               main: flag parsing, interactive prompts, exit codes
controller/
  identity/                   sets, identity.json, lock file, --fresh
  routing/                    RouteManager, state machine, gate wiring
  browser/                    Chromium launch, flags, CDP session, process tree
  startup/                    ordered startup + terminal reporter
  verify/                     V1–V9 checks, diagnostics runner
  health/                     periodic probes, DOWN/READY transitions
routing/
  provider/                   RoutingProvider, Route, Gate (SOCKS5 server)
  socks5/ http/               upstream dialers
  wireguard/                  S1 (stretch)
  windows/ macos/ linuxdev/   PlatformProvider impls
ipc/                          protocol types, server, client
browser/chromium/pin.json     pinned build manifest
browser/patches/              empty
tests/infra/                  docker-compose: ip-echo, dns-oracle, 2×socks5, 1×http proxy
tests/{browser,routing,isolation,integration}/
scripts/                      fetch-chromium, run-infra, package-{win,mac}, release
docs/                         SCOPE.md, ENGINEERING_PLAN.md, adr/, chromium-flags.md, leak-testing.md, scaling.md
README.md                     startup commands only
```

## 3. Component contracts

### 3.1 CLI

```
tab-router [--identities N] [--url URL] [--fresh] [--config P] [--routes P] [--data-dir P]
tab-router --status | --stop | --diagnostics [--full] | --version
```

Exit codes: 0 ok · 1 usage/config · 2 verification failed (shut down safely) ·
3 route establishment failed · 4 browser launch failed · 5 already running.

Interactive mode only when no flags and stdin is a TTY: prompt identity count
(default 2) and startup URL. If `routes.toml` is missing, print its path and a
pointer to `routes.example.toml`, exit 1. Never prompt for credentials.

### 3.2 IdentityManager

```
<data-dir>/identities/current -> set-<RFC3339 timestamp>
<data-dir>/identities/set-*/identity-00N/{profile/, identity.json}
<data-dir>/identities/set-*/.lock
```

`--fresh` creates a new set and repoints `current`; old sets are retained.
Missing identities within an existing set are created; never overwritten.

### 3.3 RoutingProvider / Route / Gate

```go
type RoutingProvider interface {
    Create(def RouteDef) (Route, error)
}
type Route interface {
    ID() string
    Start(ctx context.Context) error
    Stop() error
    Status() RouteStatus // CREATED STARTING VERIFYING READY DOWN STOPPED
    Dial(ctx context.Context, network, hostport string) (net.Conn, error)
    PublicIP(ctx context.Context, echoURL string) (net.IP, error)
}
type Gate interface {
    Addr() string                 // 127.0.0.1:port
    Open(); Close()               // Close also kills live tunnels
    Events() <-chan ConnectEvent  // host, atyp(DOMAINNAME|IPV4|IPV6), port, ts, outcome
}
```

State transitions are logged (credentials redacted). `READY` requires a
successful `PublicIP`.

### 3.4 BrowserManager

Launch per identity with the flag set in `docs/chromium-flags.md` (start from
§9 of the scope), `--user-data-dir=<profile>`, `--remote-debugging-pipe`,
`--no-first-run`, `--no-default-browser-check`, no `--no-sandbox`. Track the
full process tree (job object on Windows; process group on macOS) so shutdown
is total. Expose: `Navigate(identity, url)`, `Eval(identity, js)`,
`SetCookie/GetCookies`, `ResponseFor(url)`, `Kill(identity)`.

The startup URL is passed via CDP `Page.navigate`, never on the command line.

### 3.5 Verifier — startup checks

| ID | Check | How | Pass |
|---|---|---|---|
| V1 | Route up | controller `PublicIP()` via route | IP parsed |
| V2 | Browser egress | CDP hidden tab → `ip_echo_url`, read body | == V1 IP |
| V3 | Separation | all V2 IPs + host IP (one controller-side direct fetch, configurable) | pairwise distinct, none == host |
| V4 | DNS delegation | gate events for echo hostname have `DOMAINNAME`; no IP-literal CONNECT for hostname navigations | true |
| V5 | IPv6 | CDP tab → `ipv6_echo_url` | route IPv6 ≠ host IPv6, **or** blocked; never host IPv6 |
| V6 | Storage isolation | set cookie + localStorage in 001 on echo origin; read in 002; compare profile realpaths | absent in 002; distinct paths |
| V7 | Fail-closed | `Gate.Close()`; navigate → expect net error; `Gate.Open()`; navigate → success, same IP | both hold |
| V8 | No direct connections | PlatformProvider samples the identity's process-tree remote endpoints throughout V2–V7 | only `127.0.0.1:<gate>` |
| V9 | Startup URL | after `Page.navigate`, main-frame response received and gate saw CONNECT to the URL host from that identity | true |

`--diagnostics --full` adds: WebSocket echo, redirect chain (301/302/307/308,
cross-origin), download, service-worker fetch, WebRTC candidate gathering
(expect no host/srflx candidates), HTTP/3 attempt (expect fallback to
TCP via gate).

### 3.6 Terminal reporter

Content is mandatory; styling is yours. Success:

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

Failure:

```
Identity 002 → Public IP: FAILED (V2: browser egress 192.0.2.44 != route 198.51.100.7)
ERROR: Identity 002 could not be verified as isolated.
The identity will NOT use the host/default network.
Shutting down all identities...
================================
TAB ROUTER FAILED TO START SAFELY
================================
```

Exit 2. No Chromium process may survive a failed start.

### 3.7 HealthMonitor

Every `health.probe_interval_seconds` (default 10): TCP-level probe of each
upstream; every 60 s a `PublicIP()` through the route. Failure ⇒ route `DOWN`
⇒ `Gate.Close()` ⇒ terminal line `Identity 001 → Route 001 → DOWN (traffic blocked)`.
Recovery ⇒ re-run V1 ⇒ IP must equal the previously verified IP (or
`compare_host_ip` rule still holds) ⇒ `READY` ⇒ `Gate.Open()`. Also samples
V8 continuously; any non-gate endpoint is an alarm printed in red and
recorded in `--status`.

### 3.8 IPC

Unix socket (macOS, 0600) / named pipe (Windows, current-user DACL), random
per-session token in a 0600 file, newline-delimited JSON. Methods: `status`,
`identities.list`, `routes.list`, `route.start`, `route.stop`,
`identity.open_url`, `diagnostics.run`, `shutdown`. `--status`, `--stop`,
`--diagnostics` are thin IPC clients.

### 3.9 Config

`config.toml` (non-secret) and `routes.toml` (secret, owner-only, gitignored,
`password_env` supported). CLI overrides file. Identity count must be ≤ cap
and ≤ number of routes. Formats are in the scope §19. Logs redact credentials
(`socks5://user:***@host`); the log level for gate events is debug and never
includes URL paths, only hostnames.

## 4. Milestones and exit criteria

Order is deliberate. Do not start the next milestone until the exit criteria
are met and committed to `main` with green CI.

### M0 — Bootstrap
- Go module, layout above, `make lint test`, GitHub Actions matrix
  (ubuntu for logic + infra tests; windows-latest and macos-latest smoke).
- `scripts/fetch-chromium` downloads and checksum-verifies the pinned build on
  all three OSes.
- `tests/infra` docker-compose: ip-echo (HTTP+HTTPS, returns client IP as JSON),
  dns-oracle (authoritative for `test.internal`, logs resolver IP per unique
  label), two SOCKS5 proxies and one HTTP proxy each on a distinct IP of a
  test network.
- `docs/adr/0000-template.md`, empty `docs/chromium-flags.md`, `README.md`
  in the scope's format.
- **Exit:** CI green on three OSes; `docker compose up` in `tests/infra` gives
  two proxies whose egress IPs differ as seen by ip-echo.

### M1 — Two persistent identities (browser only)
- IdentityManager (sets, `identity.json`, lock, `--fresh`).
- BrowserManager launches two Chromium windows with separate profiles over
  normal networking (the only milestone where DIRECT is allowed; behind a
  build tag that is deleted in M3).
- CDP over pipe working: navigate, eval, cookies.
- **Exit:** T-D (cookie/localStorage/IndexedDB isolation) and T-E (restart
  persistence; `--fresh` retains old set) pass. Two windows visibly open and
  close cleanly with no orphan processes.

### M2 — Two routes, two public IPs (no browser)
- `RoutingProvider` interface; SOCKS5 and HTTP CONNECT upstream providers with
  auth; Gate (SOCKS5 server, `Open/Close`, `Events`).
- `PublicIP()` through each route; route state machine.
- Dev command `tab-router route-check` prints per-route IP.
- **Exit:** against `tests/infra`, two routes return two distinct IPs
  simultaneously (T-A at controller level); gate events show `DOMAINNAME` for
  hostname CONNECTs; unit tests for gate close killing live tunnels.

### M3 — Bind identities to routes + startup verification  ← primary milestone
- Chromium launched pinned to its gate with the full hardening flag set;
  `docs/chromium-flags.md` completed and each flag justified.
- Verifier V1–V8; terminal reporter (success and failure paths).
- `PlatformProvider.RemoteEndpoints(pidTree)` for the primary dev OS.
- Abort semantics: any failure ⇒ kill all ⇒ exit 2.
- DIRECT build tag removed.
- **Exit:** `tab-router --identities 2` prints `TAB ROUTER READY … PASSED`
  against `tests/infra` and against two real upstreams. Breaking route 002
  (wrong password, or stopping the proxy) prints `FAILED TO START SAFELY`,
  exits 2, leaves no Chromium. T-A, T-C, T-D, T-H, T-I pass in CI.

### M4 — Startup URL
- `--url` validation (absolute, http/https, unmodified); passed via CDP after
  verification; V9.
- **Exit:** T-F passes: two windows, two identities, two CONNECTs to the URL
  host, one per gate; `--status` shows the final URL per identity.

### M5 — Leak test suite and investigations
- `tests/isolation`: T-B (DNS oracle), V5/IPv6 paths, T-J (WebSocket,
  redirects incl. cross-origin, downloads, service-worker fetches).
- T-K: WebRTC candidate gathering and HTTP/3 behaviour under the flag set;
  packet capture on the test host during a full run.
- `docs/leak-testing.md`: method, results, and an explicit list of what is and
  is not claimed.
- **Exit:** all T-B/T-J pass; T-K findings documented; any leak found is either
  fixed or listed as a known limitation that blocks the "isolated" claim.

### M6 — Orchestration
- Controller as session daemon; IPC server; `--status`, `--stop`,
  `--diagnostics [--full]`.
- HealthMonitor with DOWN/READY transitions and live terminal updates.
- `config.toml` / `routes.toml` loading, CLI override precedence, permission
  checks (refuse to start if `routes.toml` is group/world readable),
  credential redaction, interactive mode.
- **Exit:** full startup sequence §15 of the scope runs end to end; stopping a
  proxy mid-session shows `DOWN (traffic blocked)` within one probe interval,
  the browser fails requests, restoring shows `READY` with the same IP;
  `--diagnostics` output matches scope §20.

### M7 / M8 — Windows and macOS
Do whichever is not your primary dev OS second. Per platform:
- `PlatformProvider`: connection table (Windows: `GetExtendedTcpTable`/
  `GetExtendedUdpTable` with owner PIDs; macOS: `libproc` or `lsof -a -i -p`),
  IPC endpoint (named pipe with DACL / unix socket 0600), owner-only file
  permissions, process-tree kill (job object / process group).
- Pipe-based CDP verified on the platform.
- Full suite (T-A…T-J) on a clean VM of the platform with real upstreams.
- **Exit:** the Definition of Done command passes on a clean machine of that
  platform, recorded in `docs/leak-testing.md` with the OS build and Chromium
  version.

### M9 — Scaling
- Lift the cap (config `max_identities`, default now unbounded-by-routes).
- T-G at 2, 5, 10: startup time, CPU, RSS per Chromium tree, connection
  counts, gate goroutines; results in `docs/scaling.md`.
- Verification runtime at N=10 must stay acceptable; parallelise V2–V8 across
  identities, keep V3 as the barrier.
- **Exit:** 10 identities pass verification against 10 test-infra proxies;
  practical limits documented with the resource that binds first.

### M10 — Packaging
- Windows installer (MSI or NSIS) and macOS `.dmg` bundling the pinned
  Chromium; reproducible build script with pinned toolchain and published
  checksums; release workflow in CI.
- **Exit:** a fresh Windows 11 VM and a fresh macOS 13 VM install from the
  artifacts and pass the Definition of Done command.

### Stretch
- **S1 WireGuard userspace provider** (`wireguard-go` netstack): route-owned
  DNS through the tunnel; no OS interface; same Route interface; T-A/T-B/T-C
  pass with two WireGuard peers.
- **S2 OS-level per-process egress rules** (Windows WFP; macOS Network
  Extension content filter) as a second fail-closed layer; ADR first, because
  it likely needs admin/entitlements and conflicts with rule 4.

## 5. Test matrix

| Test | Verifies | Milestone | Runs in |
|---|---|---|---|
| T-A IP separation | V2, V3 | M2/M3 | CI (infra), manual (real upstreams) |
| T-B DNS separation | V4 | M5 | CI (dns-oracle) |
| T-C Route failure | V7 | M3 | CI |
| T-D Cross-identity storage | V6 | M1 | CI |
| T-E Restart persistence | §12 | M1 | CI |
| T-F Startup URL | V9 | M4 | CI |
| T-G Concurrency | §22 | M9 | nightly |
| T-H No direct connections | V8 | M3 | CI + pcap on Win/mac VMs |
| T-I Verification reporting | §21 | M3 | CI |
| T-J Protocol coverage | §9 | M5 | CI |
| T-K WebRTC / HTTP3 | §9 | M5 | manual, documented |

CI: Linux runs logic + infra tests on every PR; Windows and macOS runners run
unit tests and a smoke start against infra reachable via a self-hosted runner
or a hosted proxy pair. Full platform suites run before tagging a release.

## 6. Known risks and what to do about them

- **Chromium implicit fallback or bypass.** Mitigation: single proxy, no
  DIRECT in the list, `<-loopback>` bypass, and T-H observation with pcap.
  If any non-gate endpoint is ever seen, treat it as a P0.
- **Chromium background traffic** (variations, component updates, safe
  browsing). It is routed, but it correlates identities via shared
  fingerprints of the same Chromium build. Disable what flags allow; document
  the rest in `docs/leak-testing.md`.
- **Public IP echo dependency.** Default to a public echo but make it
  configurable; the test infra provides a private one. Rate-limit health
  probes to avoid getting blocked.
- **IPv6 via SOCKS/HTTP proxies** depends on the upstream. The requirement is
  "route IPv6 or blocked, never host IPv6"; do not fail startup merely because
  a route has no IPv6.
- **WebRTC/QUIC.** Flags should suffice; only pcap evidence justifies a
  claim.
- **Windows job objects / macOS process groups.** Chromium spawns many
  children; orphaned renderers after a failed start would violate T-I.
- **Verification time.** V7 and V8 add seconds per identity. Run identities
  in parallel; keep the total under ~15 s for N=2.

## 7. Definition of done (copy into the release checklist)

- [ ] Clean Windows 11 and clean macOS 13+ machines run
      `tab-router --identities 2 --url https://example.com` and print
      `TAB ROUTER READY … Isolation verification: PASSED`.
- [ ] Identity 001 and 002 show distinct public IPs, both ≠ host IP.
- [ ] Storage isolation (T-D), persistence (T-E), startup URL (T-F) pass.
- [ ] DNS isolation (T-B) and IPv6 rule (V5) pass.
- [ ] Route failure blocks the identity and recovery restores it (T-C).
- [ ] No non-gate endpoints observed by connection table and pcap (T-H).
- [ ] Induced failures produce `FAILED TO START SAFELY`, exit 2, no orphans (T-I).
- [ ] WebSocket, redirects, downloads, service workers covered (T-J);
      WebRTC/HTTP3 findings documented (T-K).
- [ ] CI green on Linux, Windows, macOS; reproducible build checksums published.
- [ ] README contains only startup commands; details live in `docs/`.
- [ ] Only after all of the above: lift the cap and run M9.
