# ADR-0004: Default local exits without a user-facing login

Status: Accepted
Date: 2026-09-20
Supersedes: ADR-0003 §4 (one-time `provider login` as the normal path)

## Context

ADR-0003 introduced automatic route provisioning, but the first
implementation used Mullvad and therefore a user-facing
`tab-router provider login` step. That is a network-provider account, not a
Tab Router account, yet it still broke the intended first-run:

```
./tab-router
How many identities? 2
Startup URL: https://example.com
```

Tab Router must not have an authentication layer. It also must not pretend
it can invent public IP addresses: two isolated identities still need two
independent egress paths.

Host-level VPNs need administrator rights and couple identities together.
A commercial VPN account is real infrastructure, but it is not acceptable as
a required step in the normal product flow.

## Decision

1. Normal startup provisions routes **locally and automatically**, with no
   account, no login, and no `routes.toml`.
2. The first no-account exit mechanism is a **pinned Tor Expert Bundle**,
   fetched the same way Chromium is fetched (URL + SHA-256), run as a
   userspace daemon. Each identity gets a SOCKS5 path to that daemon with
   distinct `IsolateSOCKSAuth` credentials, so circuits (and typically
   exit IPs) differ. Chromium still talks only to its Gate.
3. This is not hidden: the terminal reports `Network provider: tor`. Traffic
   leaves through the Tor network. Some sites block Tor; that is an honest
   property of a no-account public exit, not a bug to paper over.
4. V1–V9 remain mandatory. Distinct public IPs are still proven, not assumed.
5. `routes.toml` remains a power-user override. A leftover `provider.toml`
   (Mullvad) remains an *optional* override; it is never required, never
   created by normal startup, and `--fresh` still does not touch it.
6. There is no Tab Router cloud account and no persistent cloud credential
   required for normal operation.

## Consequences

* First run is `tab-router` (or `--identities 2 --url …`). First start may
  download Chromium and Tor.
* Isolation tests keep injecting in-process SOCKS infra; they do not need a
  live Tor network.
* Live Tor is slower to bootstrap than a pre-existing VPN session and is
  blocked by some destinations. That is accepted in exchange for no login.
* Scaling to 5/10 identities remains `Provision(n)` (M9).
