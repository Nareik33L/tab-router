# Chromium launch flags and seeded preferences

Every Chromium process Tab Router starts receives the flags below
(`controller/browser/chromium.go: HardeningFlags`, `launch.go:
EnvironmentFlags`) and, for a brand-new profile, the preferences in
`controller/browser/prefs.go`. Each entry states *why* it exists so that a
future Chromium upgrade can re-evaluate it. Removing any flag in the
"isolation" group requires re-running the isolation suite and updating
this file.

## Isolation (mandatory)

| Flag | Purpose |
| --- | --- |
| `--user-data-dir=<identity>/profile` | One profile per identity: cookies, storage, cache, history are disjoint on disk. |
| `--proxy-server=socks5://127.0.0.1:<gate>` | All TCP goes to this identity's gate. The gate, not Chromium, talks to the upstream. |
| `--proxy-bypass-list=<-loopback>` | Chromium bypasses proxies for loopback by default; this removes that exception so *nothing* is direct. |
| `--host-resolver-rules=MAP * ~NOTFOUND , EXCLUDE 127.0.0.1` | Belt and braces for DNS: any attempt to resolve a hostname locally fails instead of using the host resolver. Hostnames still reach the gate unresolved (SOCKS5 DOMAINNAME). |
| `--disable-quic` | QUIC is UDP and cannot traverse a SOCKS5 CONNECT gate; disabling it prevents UDP attempts. |
| `--force-webrtc-ip-handling-policy=disable_non_proxied_udp` | WebRTC may only use proxied transports; combined with the gate (no UDP ASSOCIATE) this means no WebRTC UDP at all and no local IP enumeration. |
| `--disable-features=DnsOverHttps,AsyncDns,...` | Turns off Chromium's built-in resolver and DoH so no resolver path exists besides the upstream. Also disables background features listed below. |
| `--remote-debugging-pipe` | DevTools over an inherited pipe; no TCP listener that another process could reach. |

Seeded preference `webrtc.ip_handling_policy = disable_non_proxied_udp` and
`webrtc.multiple_routes_enabled = false` mirror the flag for profile-level
settings.

## Background traffic suppression (identity correlation)

These stop Chromium from contacting Google services on its own. Such
traffic would still go through the gate (so it is not a leak) but two
identities hitting the same endpoints with client identifiers would
correlate them.

| Flag / preference | Suppresses |
| --- | --- |
| `--disable-background-networking` | Variations seed, component updates, Safe Browsing list downloads. |
| `--disable-component-update`, `--disable-sync`, `--disable-default-apps` | Component/extension/sync traffic. |
| `--disable-domain-reliability`, `--disable-client-side-phishing-detection`, `--disable-breakpad`, `--metrics-recording-only`, `--no-pings` | Telemetry, crash reports, hyperlink auditing. |
| `--disable-features=OptimizationHints,MediaRouter,Translate,InterestFeedContentSuggestions,SafeBrowsingEnhancedProtection,ChromeWhatsNewUI,PrivacySandboxSettings4,SegmentationPlatform,AutofillServerCommunication` | Hint fetches, cast discovery (mDNS/UDP), translate ranker, feed, promo pages, Privacy Sandbox, autofill server calls. |
| `--no-first-run`, `--no-default-browser-check`, `--disable-search-engine-choice-screen`, `--no-service-autorun` | First-run UI and its network calls. |
| `--password-store=basic`, `--use-mock-keychain`, `--disable-features=OsCryptAsync,AppBoundEncryption` | No macOS login-password / Keychain prompts. Official Chromium's async OSCrypt ignores the mock keychain unless OsCryptAsync is off. |
| Preferences: `safebrowsing.enabled=false`, `signin.allowed=false`, `search.suggest_enabled=false`, `alternate_error_pages.enabled=false`, `net.network_prediction_options=2`, `spellcheck.use_spelling_service=false`, `translate.enabled=false`, `dns_prefetching.enabled=false`, `default_apps_install_state` | Same goals at the profile level so they survive a Chromium version that ignores a flag. |

## Per-identity environment (stability, not disguise)

Applied from the identity's pinned `Environment` (`identity.json`).

| Flag / preference | Setting |
| --- | --- |
| `--lang=<locale>` | UI and `navigator.language`, Intl default locale. |
| `intl.accept_languages`, `intl.selected_languages`, `spellcheck.dictionaries` | `Accept-Language` header and spellcheck consistent with the locale. |
| `TZ=<zone>` environment variable | Process timezone (honoured on macOS). |
| DevTools `Emulation.setTimezoneOverride` on every auto-attached page | Same timezone on Windows, where `TZ` is not honoured, and consistency everywhere. |
| `--window-size`, `--window-position` | Stable geometry; identities are tiled so their windows never overlap exactly. |
| `--force-device-scale-factor=<n>` | Stable DPR regardless of the display the window lands on. |
| `--force-color-profile=srgb` | Identical rendering across monitors. |
| `--force-dark-mode` (when `color_scheme = "dark"`) | Stable `prefers-color-scheme`. |
| `download.default_directory=<identity>/downloads`, `download.prompt_for_download=false` | Downloads never land in a shared folder. |

## Headless (tests and `--diagnostics` without a session)

`--headless=new --disable-gpu --hide-scrollbars --mute-audio`.

On Linux (CI/dev host only), `--disable-dev-shm-usage` is always added.
`--no-sandbox` and `--disable-setuid-sandbox` are added when running as
root, when `CI` is set, or when `TAB_ROUTER_NO_SANDBOX=1`. Product
platforms (Windows, macOS) never receive these flags.

## Environment scrubbing

Before launch the controller removes `HTTP_PROXY`, `HTTPS_PROXY`,
`ALL_PROXY`, `NO_PROXY`, `SOCKS_PROXY`, `FTP_PROXY` and `TZ` from the child
environment so nothing but the flags above can influence Chromium's
network configuration, then sets `TZ` to the identity's zone.

## Known non-goals

* User-Agent, `navigator.platform`, hardware concurrency, canvas/WebGL
  output and similar are **not** altered. Tab Router keeps identities
  separate and stable; it does not attempt to disguise them.
* Chromium still performs a handful of same-origin sub-requests for pages
  the user visits (favicons, prefetch hints declared by the page). They
  traverse the identity's gate like any other request.
