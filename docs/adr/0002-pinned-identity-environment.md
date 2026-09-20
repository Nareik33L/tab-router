# ADR-0002: Per-identity environment pinned at creation

Status: Accepted
Date: 2026-09-20

## Context

Two identities that are byte-for-byte copies of the same browser profile
behave like one browser with two cookie jars. Users need each identity to
be a consistent, independent browser environment: the same locale,
timezone, window and download location every session, chosen per identity,
never drifting with the host or between restarts. At the same time the
product explicitly does not spoof fingerprints.

## Decision

* `identity.Environment` (locale, Accept-Language, timezone, window
  geometry, device scale factor, colour scheme, download directory) is
  computed once when an identity is created, validated, written to
  `identity.json`, and thereafter read from there. Config values only
  influence creation; existing identities ignore config changes.
* Defaults come from the host at creation (locale, timezone) and are
  pinned so a later host change does not alter the identity.
* Values are applied through Chromium's own knobs: flags (`--lang`,
  `--window-size/position`, `--force-device-scale-factor`,
  `--force-color-profile`, `--force-dark-mode`), seeded preferences, `TZ`,
  and a DevTools `Emulation.setTimezoneOverride` on every auto-attached
  page so Windows (which ignores `TZ`) behaves identically.
* No User-Agent, platform, hardware or graphics values are altered.

## Consequences

* Restart stability is testable: `TestPersistenceAndFresh` and
  `TestEnvironmentApplied` assert identical observed values across
  launches.
* Changing an identity's environment is a deliberate act (edit
  `identity.json` while stopped, or `--fresh`), never a side effect.
* Because the timezone override is applied via DevTools auto-attach, every
  page target waits for the controller before running script; the
  controller must stay attached for the browser's lifetime, which it does
  anyway for health monitoring.
