# ADR-0003: Automatic route provisioning via userspace WireGuard

Status: Accepted
Date: 2026-09-20

## Context

v0.2 required the user to write `routes.toml` with one SOCKS5 or HTTP CONNECT
upstream per identity. That is a real network-exit mechanism, but it is not
the intended product UX: the user should choose an identity count and a URL,
and Tab Router should create, verify, bind and tear down the routes.

The application cannot invent public IP addresses. Any automatic provisioner
must therefore talk to an actual network-exit provider. Host-level tunnels
(utun, wintun, routing-table changes) need administrator rights and would
couple identities together, violating fail-closed isolation.

## Decision

1. A `Provisioner` creates **N independent routes** for N identities
   (`Identity i → routes[i]`). The first product milestone remains two
   identities; the interface is not hard-coded to two.
2. The first automatic provider is **Mullvad**, because a single account
   can register multiple WireGuard devices and the public relay list lets
   us pin each identity to a different egress IP.
3. Each route is a **userspace** WireGuard tunnel (`wireguard-go` + gvisor
   netstack). UDP to the relay is sent by the controller process. There is
   no OS tunnel interface, no host route, no DNS change, and no
   administrator rights. Chromium still talks only to its local Gate; it
   never sees WireGuard, credentials, or relay addresses.
4. One-time setup is `tab-router provider login`. The account number and
   per-slot device keys live owner-only in `provider.toml`. `--fresh`
   rotates browser identity sets and does not touch this file.
5. Routes are provisioned fresh on every start and torn down on exit. A
   failed route is re-established onto a different relay when the provider
   can supply one; the gate stays CLOSED until the replacement's egress is
   verified and is never allowed to share another identity's path.
6. An existing `routes.toml` remains a power-user override (tests, bring
   your own upstreams). Explicit `--routes` wins; otherwise `provider.toml`;
   otherwise `routes.toml`; otherwise the CLI prints the login hint and
   exits 1. Automatic provisioning is not considered successful until the
   existing V1–V9 checks pass.

## Consequences

* Normal startup is `tab-router` then identities + URL. No proxy hostnames
  or SOCKS credentials in the user flow.
* A Mullvad account is required for automatic egress. That is a network
  provider the user already pays, not a Tab Router cloud service; browsing
  data still never leaves the device except through the chosen routes.
* Device slots consume the account's Mullvad device limit (typically five).
  Hitting the limit is reported with a link to manage devices.
* Isolation tests keep using in-process SOCKS/HTTP infra via the same
  `Provisioner` interface. Live Mullvad is exercised by `provider login`
  plus `route-check` on a real account.
* Scaling to 5 or 10 identities is `Provision(n)` once the 2-identity
  isolation suite is signed off (M9); no new architecture.
