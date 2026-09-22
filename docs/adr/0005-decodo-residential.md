# ADR-0005: Decodo residential exits behind activation

Status: Accepted
Date: 2026-09-22

## Context

The default no-login path is a local Tor exit (ADR-0004). Some sites treat
every Tor exit as a proxy and refuse it. A residential exit is a different
upstream, not a way to disguise Tor.

Decodo assigns the residential IP. Tab Router does not download a pool of
addresses. It opens one sticky SOCKS5 session per identity.

## Decision

1. Decodo is an explicit provider (`[routing] provider = "decodo"` or
   `TAB_ROUTER_PROVIDER=decodo`). It is not the default. When it is
   selected and cannot be built, startup fails. It does not fall through
   to Tor, Mullvad, or `routes.toml`.
2. `Decodo.Provision(n)` accepts any `n >= 1`. The product cap remains
   `MaxIdentities` in config. The provisioner does not assume two.
3. Each slot gets its own alphanumeric session id and a username of the
   form documented by Decodo (September 2026):

   `user-ACCOUNT[-country-cc]-session_iplock-SESSION-sessionduration-MINUTES`

   Gateway: `gate.decodo.com:7000` (SOCKS5). `session_iplock` is used
   instead of `session` because Decodo rotates a plain session when the
   residential peer drops, and fail-closed requires that request to fail.
   Sticky duration defaults to 1440 minutes, Decodo's maximum.
4. `Reestablish` reconnects the same session id. A new id is created only
   when the sticky window has expired, or once at startup if two identities
   observe the same public IP (`ReplaceSession`). A later IP change still
   fails closed.
5. The desktop binary does not contain a Decodo password. Local development
   may set `DECODO_USERNAME` and `DECODO_PASSWORD`. Otherwise the activation
   client exchanges a key for an installation token. The backend returns
   limited proxy access on activate and validate. The token is stored in
   the macOS login keychain, or in an owner-only file on other systems.
   The proxy password is not written to that store; it is fetched again
   each start. If the activation server is unreachable, startup stops.
6. Chromium still talks only to its local gate. The gate dials Decodo.

## Consequences

* Operators run `cmd/activation-server` with the limited Decodo account in
  the server environment, or set per-license proxy credentials at issue
  time. Keys are stored as hashes.
* A dead residential IP fails the route instead of silently moving the
  browser to another address.
* Tor remains the path when no provider is configured.
