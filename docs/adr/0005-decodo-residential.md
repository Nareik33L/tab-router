# ADR-0005: Decodo residential exits

Status: Accepted
Date: 2026-09-22

## Context

Sites that refuse Tor exits need a residential upstream. Decodo assigns
that address. Tab Router does not download a pool of addresses. It opens
one sticky SOCKS5 session per identity.

Tor (ADR-0004) is no longer selected at startup.

## Decision

1. Startup always uses Decodo. Missing credentials fail closed. Startup
   does not select Tor, Mullvad, or a leftover `routes.toml`. Explicit
   route definitions (`--routes`, and tests that pass `RouteDefs`) still
   win so isolation checks can run without a live account.
2. `Decodo.Provision(n)` accepts any `n >= 1`. The product cap remains
   `MaxIdentities` in config. The provisioner does not assume two.
3. Each slot gets its own alphanumeric session id and a username of the
   form documented by Decodo (September 2026):

   `user-ACCOUNT[-country-cc]-session_iplock-SESSION-sessionduration-MINUTES`

   Gateway: `gate.decodo.com:7000` (SOCKS5). `session_iplock` is used
   instead of `session` because Decodo rotates a plain session when the
   residential peer drops, and fail-closed requires that request to fail.
   Sticky duration defaults to 1440 minutes, Decodo's maximum. Country is
   optional and is not hard-coded.
4. `Reestablish` reconnects the same session id. A new id is created only
   when the sticky window has expired, or once at startup if two identities
   observe the same public IP (`ReplaceSession`). A later IP change still
   fails closed.
5. The desktop binary does not contain a Decodo password. An interactive
   start asks for the proxy username and password before access. The
   password is not echoed, not written to the activation store, not logged,
   and not placed in the process environment for Chromium. When both
   `DECODO_USERNAME` and `DECODO_PASSWORD` are set, the prompt is skipped.
   A non-interactive start without both values fails closed.
6. Chromium still talks only to its local gate. The gate dials Decodo.

## Consequences

* The person running Tab Router needs the Decodo residential proxy
  username and that proxy user's password. The website login is not used.
* A dead residential IP fails the route instead of silently moving the
  browser to another address.
* Tor is not started by product startup.
