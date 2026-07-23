# Warp Command Experience — M2b: Gutter-geometry feasibility spike (de-risk 4ff.5)

> **Research / de-risking task.** The deliverable is a findings doc + recommendation + a minimal throwaway harness — NOT production feature code. Time-box it.

**Goal:** Settle *how* nocx will draw a per-command status glyph in a left "gutter" over the xterm.js grid, so the blocks milestone (`nocx-4ff.5`) can be planned with a proven approach instead of an open question.

**Why:** The blocks spec flags this as unproven (Codex review). `registerDecoration({x:0,width:1})` reserves a cell *inside* the grid; naively translating that element left may land it in a clipped layer and drift on fit/DPI/font/renderer-fallback. VS Code's terminal solves the same problem — learn from it.

## Global Constraints

- **Isolated files only.** Create ONLY new files under `frontend/src/spikes/` (e.g. `frontend/src/spikes/gutter-spike.ts`) and the findings doc `docs/superpowers/specs/2026-07-23-gutter-geometry-findings.md`. Do NOT modify `xterm.ts`, `tabs.ts`, `main.ts`, `style.css`, or any shared file — another worker is editing the tree in parallel.
- **Do NOT run `npm install` or `npm run build` or the full suite** — deps are installed; the coordinator verifies at the end. You MAY run a single test file you create.
- **Commit only your own files** with explicit paths; retry after 2s on `.git/index.lock`.

## Questions to answer (cite sources: xterm typings in `frontend/node_modules/@xterm/xterm/typings/xterm.d.ts`, and public xterm.js / VS Code behaviour)

1. **API surface:** In xterm.js 5.5, what exactly does `terminal.registerDecoration(options)` accept and render? Document `IDecorationOptions` (`marker`, `x`, `width`, `overviewRulerOptions`, `layer`, `anchor`?) and what `decoration.onRender(el)` gives you. Where in the DOM does the decoration element live, and what clips it (the screen element? the decorations container?).
2. **Gutter placement:** Can a decoration element be positioned in the LEFT margin (a gutter left of column 0) without being clipped? Test three approaches and record which actually works:
   - (a) decoration at `x:0` + CSS `transform: translateX(-100%)` into the terminal's left padding;
   - (b) a dedicated sibling gutter `<div>` overlaid on the terminal container, with each glyph's `top` computed from the marker line and the viewport scroll (like a code editor's gutter);
   - (c) reserve real left padding on the terminal container and place the glyph there, accounting for it in `FitAddon`.
3. **Robustness:** For the chosen approach, what happens on `fit()`/resize, DPI change, font-load reflow, WebGL→Canvas→DOM renderer fallback, and scrollback trim (`marker.onDispose`)? Which approach survives all of these with least code?
4. **Prior art:** How does VS Code's terminal render its command-status gutter decorations (shell integration dots)? Summarize the technique and whether it maps to our renderer-agnostic boundary (AD-6: policy above the renderer).

## Deliverable

1. `frontend/src/spikes/gutter-spike.ts` — a minimal harness that mounts an xterm `Terminal`, registers a marker + a decoration using the RECOMMENDED approach, and exposes it so it can be eyeballed later (export a `mountGutterSpike(container)` function). Headless layout is not fully verifiable in jsdom, so also add whatever `getBoundingClientRect`/DOM-structure assertion is feasible in a `frontend/src/spikes/gutter-spike.test.ts` (even if limited to API-shape / element-presence checks) and note the limitation.
2. `docs/superpowers/specs/2026-07-23-gutter-geometry-findings.md` — answers to Q1-Q4, a clear **RECOMMENDATION** (which of (a)/(b)/(c), with the reasoning), the exact xterm API calls the blocks milestone should use, and any risks that still require in-app visual confirmation.

Report `worker_done` with the findings-doc path and a one-line recommendation summary.
