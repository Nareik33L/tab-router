# ADR-0001: One Chromium process per identity behind a controller-owned gate

Status: Accepted
Date: 2026-09-20

## Context

The product must guarantee that traffic from identity A never leaves via
identity B's route or the host connection, including when a route fails.
Chromium offers `--proxy-server` per *process* and per-context proxy
settings only through DevTools or extensions, which are advisory: a
misconfiguration or a Chromium regression could silently fall back to
direct connections. A single Chromium process with several contexts also
shares a socket pool, HTTP/2 sessions, the certificate cache and GPU
process, giving correlation and leak surface.

## Decision

1. Each identity is a separate Chromium process tree with its own
   `--user-data-dir`.
2. Each process is pointed at a **gate**: a local SOCKS5 server owned by
   the controller, bound to loopback, that forwards only while its route is
   `READY`. When the route is anything else the gate refuses CONNECT with a
   SOCKS failure reply and terminates live tunnels.
3. The gate is the only place upstream credentials exist. Chromium is
   never given them.
4. Hostname resolution is delegated to the upstream (SOCKS5 DOMAINNAME /
   HTTP CONNECT host); Chromium is additionally forbidden from resolving
   locally via `--host-resolver-rules`.
5. Startup verification and the runtime monitor inspect the process
   tree's sockets from the OS side to confirm the only remote endpoint is
   the gate.

## Consequences

* Fail-closed is a structural property: the browser has no configured path
  except the gate, and the gate has no forwarding path except its route.
* Memory cost is one Chromium per identity, acceptable for two identities
  and revisited before the cap is lifted (M9).
* UDP (QUIC, WebRTC media) is unsupported by design; the gate rejects UDP
  ASSOCIATE and Chromium is configured not to attempt it.
* Presenting identities as tabs in one window is out of scope for v0.1;
  windows are tiled per identity instead.
