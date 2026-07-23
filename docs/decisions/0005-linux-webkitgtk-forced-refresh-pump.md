# ADR-0005 — Linux/WebKitGTK: periodic forced-refresh pump

- **Status:** Accepted
- **Date:** 2026-07-23
- **Related:** GitHub issue `shady2k/nocx#2`, `ADR-0001`

## Context

On Linux, nocx runs inside a Wails v2 webview, which on Linux is **WebKitGTK**
(not Chromium — Wails v2 and v3 offer no Linux Chromium backend; see the research
in this issue thread). On some WebKitGTK + GPU setups the compositor does not
present a frame until the window receives a user interaction (click/focus),
and even then frames lag one behind. Because xterm.js schedules every render
through `RenderDebouncer` → `window.requestAnimationFrame`, a stuck compositor
means the rAF callback never runs and the repaint never happens.

The user-visible symptoms (issue #2):

1. **Initial shell prompt invisible until click** — the prompt is PTY output
   written to the xterm.js buffer, but its rAF-scheduled repaint never runs.
2. **Last typed character not displayed** — each keystroke's echo renders one
   frame behind, so the most recent character is stuck waiting for the next
   input event that pumps a frame.
3. **Slow input rendering** — the one-frame-behind lag is perceptible.

## Approaches measured and rejected

All were built, run on the target Linux box (X11, Intel Iris Xe / i915,
WebKitGTK 2.52.3, Wails v2.13), and verified with screenshots (terminal-region
bright-pixel fraction before vs. after a click, and after typing):

- **`WEBKIT_DISABLE_DMABUF_RENDERER=1` env var (main.go)** — fixed it on an
  Intel i915 in the repo's test environment, but did **not** fix it on the
  user's machine. Env reaches the `WebKitWebProcess` (confirmed via
  `/proc/<pid>/environ`), so it is applied; the compositor is still stuck.
  Kept as a no-op here; the env var is not a reliable fix.
- **DOM renderer (`?r=wterm`)** — identical bug. The problem is compositor
  presentation, not the renderer, so switching engines inside the same
  WebKitGTK webview changes nothing.
- **Dual-drive rAF** (`requestAnimationFrame` + `setTimeout(32ms)` race, who
  fires first wins) — the rAF callback ran, but the WebGL/canvas layer still
  did not composite without a user event. The render executed; the
  presentation did not.
- **Tabby-style synchronous `_core._renderService._renderRows()` after each
  write** — xterm.js 5.5 no longer exposes `_core` on `Terminal` (the internal
  used by Tabby, which targets xterm 5.4). The internal is gone; the approach
  is not portable to our version.

## Decision

**A periodic forced-refresh pump, Linux/WebKitGTK only.** Every 42 ms
(~24 fps) the pump calls `term.refresh(0, rows-1)`, which re-marks every row
dirty and schedules a render. On a stuck compositor this keeps a render
perpetually pending, so the buffer becomes and stays visible without a click.

- **Platform-gated** by `isLinuxWebKit()` (`navigator.platform` is Linux and
  the UA contains "WebKit"). On macOS (WKWebView) and in plain browsers the
  pump is never installed — the compositor is healthy and the CPU cost is
  wasted.
- **42 ms** — smooth enough for terminal output, well below the perception
  threshold, and cheap when nothing changed (a no-op refresh on an
  unchanged buffer costs little).
- **Lifecycle** — the `setInterval` is held on the renderer and cleared in
  `dispose()`, called from `Tab.close()`, so the pump does not outlive the
  terminal it paints and does not leak across tab open/close cycles.

This is a **workaround for an upstream WebKitGTK compositor bug**, not a
change to nocx's architecture. The WebSocket protocol, the data plane, and the
renderer seam (`TerminalRenderer`) are untouched.

## Evidence

Measured on the user's Linux box (X11, Intel i915, WebKitGTK 2.52.3), using
`import -window <id>` screenshots and a bright-pixel fraction over the
terminal region (bg `#1a1b26` dark, prompt text `#c0caf5` light):

| State | bright fraction |
|---|---|
| No pump, no click | 0.0002 (blank) |
| No pump, after click | 0.0020 (prompt appears) |
| No pump, after typing `abc` | 0.0020 (last char missing) |
| **Pump, no click** | **0.0052 (prompt visible)** |
| Pump, after typing `abc` | 0.0021 |
| Pump, after typing `Z` | 0.0021 (last char painted) |

The blank-to-visible transition happens without any user interaction, and
every typed character is painted immediately.

## Consequences

- The initial prompt and the last typed character are visible on Linux
  without a click — issue #2 resolved.
- A ~24 Hz timer runs per xterm.js tab on Linux/WebKitGTK only. On macOS and in
  plain-browser dev the pump is absent.
- `TerminalRenderer` gains a `dispose()` method (the pump must be cleared on
  tab close). `WtermRenderer` implements it as a no-op; the test fixture
  provides a `vi.fn()`.
- This does **not** fix WebKitGTK; it works around a presentation bug from the
  frontend. If Wails ever ships a Linux Chromium backend, or WebKitGTK 6.0
  (the Wails v3 default) happens to fix the compositor, the pump becomes
  dead weight on that platform and can be removed.

## Revisit when

- **Wails offers a Linux Chromium backend.** As of Wails v3 alpha this does
  not exist (the maintainer calls a CEF/Qt WebEngine port "extremely
  non-trivial"; the one draft PR was closed unmerged). If it ships, the
  `isLinuxWebKit()` gate will return false for it and the pump will not
  install — no code change required.
- **WebKitGTK 6.0 / GTK4** (Wails v3 default) is tested and found to fix the
  compositor stall. Then the pump is unnecessary on that version; narrow the
  gate or remove it.
- **xterm.js re-exposes a synchronous render path** (`_core._renderService`
  or an equivalent public API). An event-driven repaint after each write would
  be cheaper and lower-latency than a 42 ms poll and should replace the pump.