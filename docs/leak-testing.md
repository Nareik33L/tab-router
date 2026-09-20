# Leak testing

Automated checks (V1–V9 at every start, `tests/isolation` in CI) are the
first line. This document is the manual procedure used before a release and
whenever Chromium is upgraded: it observes the machine from *outside* the
browser with a packet capture, so a Chromium change that bypasses the proxy
configuration would still be caught.

## Setup

1. A Mullvad account (`tab-router provider login`) so two identities get two
   public IPs. For an offline run use the test network instead:
   `go run ./tests/infra/cmd/tab-router-infra --out /tmp/tr` (egress
   `127.0.0.2` / `127.0.0.3`; on macOS first `sudo ifconfig lo0 alias
   127.0.0.2 up` and likewise for `.3`).
2. A capture on the host's real interface (Wireshark or `tcpdump -i <if>
   -w tr.pcap`), started **before** Tab Router.
3. Note the host's public IP (`curl https://api.ipify.org`).

## Procedure

| Step | Action | Expected |
| --- | --- | --- |
| 1 | `tab-router --identities 2 --url https://example.com` | Terminal reaches `TAB ROUTER READY`; egress IPs differ from each other and from the host. |
| 2 | In the capture, filter `ip.dst != <upstream-A> && ip.dst != <upstream-B> && !dns && !arp` | Only traffic from the controller's single host-IP probe (`api.ipify.org`, once) and OS background noise unrelated to Chromium. No Chromium flows. |
| 3 | Filter `dns` | No queries for hostnames typed into either browser. Queries for `api.ipify.org` from the controller are expected once. |
| 4 | Filter `udp && !dns && !mdns` | No UDP from Chromium processes (no QUIC, no STUN/TURN). |
| 5 | In identity 001 visit `https://browserleaks.com/webrtc` | No public or local IP revealed by WebRTC; only the route's IP via HTTP. |
| 6 | In both identities visit `https://ipv6.icanhazip.com` | Either blocked, or an IPv6 belonging to the route. Never the host's IPv6. |
| 7 | Kill upstream A (or `tab-router` IPC `route.stop` slot 1) | Within `probe_interval` the terminal prints `Identity 001 → Route 001 → DOWN (traffic blocked)`; identity 001 pages fail with `ERR_SOCKS_CONNECTION_FAILED`; identity 002 unaffected. Capture shows no new flows from Chromium #1 to anything but the loopback gate. |
| 8 | Restore upstream A | `READY: egress <IP-A> verified`; browsing resumes with the same IP. |
| 9 | Open a WebSocket test page (e.g. `https://websocketking.com`) and connect to `wss://echo.websocket.org` in identity 002 | Works; capture shows the flow only to upstream B. |
| 10 | Download a file in each identity | Files land in `<identity>/downloads/`, fetched through the route. |
| 11 | `tab-router --status` | Both identities `READY`, no `ALARM` lines. |
| 12 | `tab-router --stop` | All Chromium processes gone (`pgrep -f user-data-dir` / Task Manager). |
| 13 | Restart with the same set | Cookies/logins persist per identity; `Environment` lines in `--status` are unchanged. |

## Reading the capture

* Every Chromium flow must have destination = loopback gate (`127.0.0.1:<port>`).
  The gate → upstream flows are the controller's and should be the only
  off-host traffic besides the one host-IP probe.
* Any DNS query for a hostname you typed is a **failure**: the browser
  resolved locally.
* Any UDP with a Chromium PID as source is a **failure**.

Record results in the release notes with the Chromium version and the
capture file hash.
