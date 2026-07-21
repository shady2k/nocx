# ADR-0001 — xterm.js as the VT frontend

- **Status:** Accepted
- **Date:** 2026-07-21
- **Supersedes:** the ghostty-web assumption baked into AD-5/AD-6 and the Overview
- **Related:** `nocx-dej` (spike), `nocx-0iz` (bake-off), `nocx-4kt`, `nocx-l8z`, `nocx-id3`

## Context

The architecture named **ghostty-web** as the VT frontend before anything was
measured, and AD-5/AD-6 rested on an explicit `[ASSUMPTION]` that it surfaces
OSC 7 and OSC 133 as events. That assumption was the documented **top risk**: if
false, the fallback is backend OSC parsing, which breaks AD-6's "the backend does
not sniff the byte stream" invariant.

A tabbed A/B harness (`nocx-0iz`) put three candidates side by side, each on its
own live PTY: ghostty-web (WASM VT → canvas-2d), xterm.js (WebGL → GPU atlas),
and wterm (DOM).

## Decision

**xterm.js is the VT frontend.** ghostty-web and wterm stay as switchable
renderers behind the `TerminalRenderer` interface so any future candidate can be
re-tested cheaply.

## Evidence

Measured, not read off documentation:

1. **OSC handlers — the decisive axis.** xterm.js exposes
   `parser.registerOscHandler`. Registering handlers and writing real sequences
   returned OSC 7 as `file://…/repos/nocx` and OSC 133 as all five markers
   (`A`, `B`, `C`, `D;0`, `D;127`), exit codes included. ghostty-web exposes **no
   OSC handler registration at all** — only OSC 8 hyperlinks, and its own types
   note the URI is "not yet exposed in simplified API".
2. **Rendering.** ghostty-web is canvas-2d only (zero WebGL/GPU references in the
   package; 0.4.0 is the latest published version). It fills each cell as its own
   rect in CSS pixels under `ctx.scale(dpr)`, so cell edges land between physical
   pixels and antialias into visible seams. Reproduced in isolation without WASM:
   adjacent rects of 8.4 CSS px at DPR 2 seam, 9.0 CSS px are solid.
3. **Scroll.** ghostty-web resets the viewport to the bottom on new output, so
   scrollback is unusable under a TUI whose status line ticks (`nocx-l8z`). wterm
   translates the wheel into arrow keys, which prompt_toolkit reads as history
   navigation (`nocx-id3`). xterm.js is correct on both.

## Consequences

- AD-5/AD-6 hold as written: OSC parsing stays frontend-side, the backend never
  sniffs the byte stream. The conditional dependency in AD-6 is discharged.
- The top risk and the `[ASSUMPTION]` markers are resolved and removed.
- Warp-style features (command blocks, cwd awareness, duplicate-tab-in-cwd) are
  unblocked — they ride on OSC 133/OSC 7, which are now proven reachable.
- ghostty-web remains a dependency for the bake-off tabs, so its MIT attribution
  obligation (© 2025 Coder) is unchanged.

## Revisit when

- A GPU renderer ships for `libghostty-vt` in the browser. None exists today:
  every web project on that core renders canvas-2d or DOM, and `vscode-bootty`
  lists WebGL as "a potential future enhancement". Because ghostty-web
  deliberately mirrors the xterm.js API and both sit behind `TerminalRenderer`,
  swapping back costs one file in `src/renderers/`.
- Native-Ghostty rendering quality becomes a hard product requirement. That is
  not a renderer swap but an AD-3 change: embedding full `libghostty` requires a
  native macOS shell instead of the Wails webview.
