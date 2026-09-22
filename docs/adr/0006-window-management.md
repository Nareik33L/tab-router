# ADR-0006: Optional arrangement of the existing Chromium windows

Status: Accepted
Date: 2026-09-22

## Context

Each identity is already its own Chromium process and its own window
(ADR-0001). Launch positions are staggered, so the windows overlap and can
be dragged. A single shared window is still out of scope.

## Decision

1. Window management only moves, resizes, and focuses those existing
   windows. It does not change the process model, the gate, or the pinned
   identity environment.
2. The default is manual. Windows stay where the user puts them, including
   when one closes. `tab-router windows tile` packs every live window onto
   the usable area of the chosen display. There is no minimum cell size.
   The grid follows the display's aspect: wide displays get more columns,
   tall ones more rows.
3. `restore` returns the positions from before the tile. `manual` turns
   auto-arrange off and does not move anything. Auto-arrange, when enabled,
   rebuilds the grid as windows come and go.
4. The display list includes the work area inside the taskbar, dock, and
   menu bar, in the pixel space Chromium uses for window bounds. Windows
   are kept inside that area. The chosen display is remembered.
5. Focus next and focus previous are optional shortcuts, off unless
   configured. Focusing a window does not move the others. Tiling focuses
   the window that was already focused, so a very small cell stays
   identifiable. None of these operations move the pointer.

## Consequences

* `tab-router windows …` talks to the running session over the local
  control channel.
* Headless sessions have no windows to arrange.
* Global shortcuts are best-effort: Windows registers them with the shell,
  macOS and Linux use the display server. If registration fails, the
  commands still work.
