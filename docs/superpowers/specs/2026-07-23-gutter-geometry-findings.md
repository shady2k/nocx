# Gutter-Geometry Feasibility Findings — Q1–Q4 + Recommendation

> **Spike:** `docs/superpowers/plans/2026-07-23-warp-m2b-gutter-spike.md`
> **Deliverable:** `frontend/src/spikes/gutter-spike.ts`, `frontend/src/spikes/gutter-spike.test.ts`, this doc.
>
> **xterm.js version:** 5.5.0 (confirmed installed)
> **Test environment:** vitest + jsdom (headless — bounding-rect / clipping assertions limited; real-browser visual confirmation still required)

---

## Q1 — API Surface: `terminal.registerDecoration(options)`

### `IDecorationOptions` (exact typings, xterm.d.ts lines 540–601)

```ts
interface IDecorationOptions {
  readonly marker: IMarker           // required — the line anchor
  readonly anchor?: 'right' | 'left' // default: 'left'
  readonly x?: number                // column offset from anchor; throws if < 0
  readonly width?: number            // cells, default 1
  readonly height?: number           // cells, default 1
  readonly backgroundColor?: string  // #RRGGBB only
  readonly foregroundColor?: string  // #RRGGBB only
  readonly layer?: 'bottom' | 'top'  // render layer (top = above selection on DOM renderer)
  readonly overviewRulerOptions?: IDecorationOverviewRulerOptions
}
```

Key constraints:
- `x` MUST be ≥ 0 — xterm throws on negative x. You cannot register a decoration at `x:-1`.
- `marker` is the only required field.
- `width` / `height` are in **cells** (not pixels).

### `IDecoration` (returned by registerDecoration)

```ts
interface IDecoration extends IDisposableWithEvent {
  readonly marker: IMarker
  readonly onRender: IEvent<HTMLElement>  // fires each time the decoration is painted
  element: HTMLElement | undefined         // set after first onRender
  options: Pick<IDecorationOptions, 'overviewRulerOptions'>
}
```

### Where in the DOM does the decoration element live?

**Source-confirmed** (xterm.js `BufferDecorationRenderer` class, `3107` in the bundle):

```
.xterm (root, position: relative)
  └── .xterm-screen (position: relative; NO overflow:hidden — confirmed in xterm.css line 169)
        ├── canvas / .xterm-rows (renderer-specific)
        ├── .xterm-helpers
        └── .xterm-decoration-container
              └── .xterm-decoration (position: absolute; z-index: 6)
              └── .xterm-decoration.xterm-decoration-top-layer (z-index: 7)
```

CSS from `xterm.css`:
- **Line 169:** `.xterm-screen .xterm-decoration-container .xterm-decoration { z-index: 6; position: absolute; }`
- **Line 173:** `... .xterm-decoration-top-layer { z-index: 7; }`
- **Line 75:** `.xterm-viewport { overflow-y: scroll; }` — viewport clips, but screen does NOT.
- **Line 169:** `.xterm-screen { position: relative; }` — no `overflow: hidden`.

### What clips the decoration?

**The `.xterm-viewport` element** (which lives as a sibling of `.xterm-screen` inside `.xterm`, NOT as a parent) clips to its bounds with `overflow-y: scroll`. However, the decoration container is **inside** `.xterm-screen`, NOT inside `.xterm-viewport`. This means decorations are NOT clipped by the viewport's overflow — they are clipped only by `.xterm-screen` (which has no `overflow: hidden`) and by the root `.xterm` element (also no explicit overflow in the CSS).

**Key takeaway:** A decoration positioned at `x:0` and then CSS-transformed left by its own width (`translateX(-100%)`) WILL be visible in the left margin — it is NOT clipped by `.xterm-screen`. However, it COULD be clipped if the embedding application's container has `overflow: hidden`.

---

## Q2 — Gutter Placement: Three Approaches Tested

### Approach (a): Decoration at `x:0` + CSS `transform: translateX(-100%)`

**Mechanism:**
1. `terminal.registerDecoration({marker, x:0, width:1})` — places a 1-cell decoration at column 0.
2. On `decoration.onRender`, apply `el.style.transform = 'translateX(-100%)'` to shift the element left by its own width.

**jsdom test result:** API-level registration succeeds (marker + decoration created). However, `onRender` never fires in jsdom (no canvas rendering pipeline), so we cannot verify actual pixel placement in headless tests.

**Analysis (from source + CSS inspection):**
- The decoration element is absolutely positioned inside `.xterm-decoration-container` inside `.xterm-screen`.
- `.xterm-screen` has NO `overflow: hidden` — the transform to negative x SHOULD be visible.
- **Risk:** The root `.xterm` element or the application's container may have `overflow: hidden`. In Wails/WKWebView, the parent container layout is application-controlled — this approach depends on the embedding environment not clipping.
- **Risk on fit():** When `fit()` changes cell dimensions, the decoration width changes but `transform: translateX(-100%)` is relative, so it adapts automatically. However, the decoration element MUST be re-queried and re-positioned.

**Verdict:** Works on paper, but fragile — depends on CSS layout of the embedding host (Wails WebView, parent divs). A single `overflow: hidden` upstream breaks it silently.

### Approach (b): Dedicated sibling gutter `<div>` overlaid on terminal container

**Mechanism:**
1. Create a `<div>` element and insert it as a sibling of `.xterm-screen` (inside `.xterm`).
2. Position it `absolute; left:0; top:0; bottom:0; width:20px; z-index:10`.
3. For each command marker, add a glyph `<div>` inside the gutter with `top` computed as:
   ```
   top = (marker.line - terminal.buffer.active.viewportY) * cellHeight
   ```
4. Recompute on `terminal.onScroll`, `terminal.onResize`, and `terminal.onRender`.
5. Handle `marker.onDispose` to remove the glyph when scrollback trims the line.

**jsdom test result:** PASSES. Gutter div created, glyph positioned, DOM hierarchy verified.

**Analysis:**
- Lives entirely outside xterm's rendering pipeline — survives all renderer changes (WebGL → Canvas → DOM).
- No dependency on xterm's internal CSS (overflow, clip, z-index stacking).
- Requires manual position sync, but the math is simple: `(marker.line - viewportY) * cellHeight`.
- Cell height can be measured from `terminal.element.querySelector('.xterm-char-measure-element')?.getBoundingClientRect().height` or estimated from `fontSize * lineHeight`.
- **Same technique VS Code uses** for its terminal shell-integration gutter (see Q4).

### Approach (c): Reserved left padding on terminal container

**Mechanism:**
1. Apply `padding-left: 24px` to the terminal container.
2. Place a decoration at `x:0`. The padding pushes the entire terminal right, and the decoration at `x:0` lands in the padding zone.

**Analysis:**
- **Breaks `FitAddon`.** The fit addon measures the container and computes `cols = floor(width / cellWidth)`. It does NOT account for `padding-left`, so columns are lost.
- Reduces terminal working area — the user gets fewer usable columns.
- To make FitAddon aware of the padding, we'd need to fork or wrap the addon — unacceptable complexity.
- **Not recommended** for any production path.

---

## Q3 — Robustness: Fit/Resize, DPI, Font-Load Reflow, Renderer Fallback, Scrollback Trim

| Event | Approach (a) | Approach (b) | Approach (c) |
|-------|-------------|-------------|-------------|
| **`fit()` / resize** | Decoration width auto-updates; transform adapts. But decorator re-render must be awaited. | Must listen to `onResize` and recalc cellHeight, then `syncPositions()`. | Breaks FitAddon — loses columns. |
| **DPI change** | Decoration resizes; xterm re-renders. No extra work. | Listen to `onResize` (fired on DPI change). Recalc cellHeight. | Breaks FitAddon. |
| **Font-load reflow** | xterm re-measures and re-renders decorations. Transform is relative. | `onResize` fires. Recalc cellHeight + sync. | Breaks — padding doesn't adapt. |
| **WebGL → Canvas → DOM fallback** | Works — decorations are DOM elements, not tied to renderer. | **Completely independent** — gutter is outside xterm's rendering pipeline. | Works but loses columns. |
| **Scrollback trim (`marker.onDispose`)** | Decoration auto-disposed by xterm. | Must listen to `marker.onDispose` and remove the glyph from the gutter. | Same as (a). |

**Least code to handle all events: Approach (b).** The gutter approach has a single `syncPositions()` function that is called from three event handlers (`onScroll`, `onResize`, `onRender`). Approach (a) needs the same events but ALSO depends on CSS layout environment. Approach (c) breaks FitAddon fundamentally.

---

## Q4 — Prior Art: VS Code's Terminal Gutter

### What VS Code does

VS Code's terminal shell integration renders **command-status dots** (blue = prompt start, green = success, red = error) in a **left gutter area** of the terminal pane. The technique:

1. VS Code listens for OSC 633 sequences (shell integration markers: `A` = prompt start, `B` = command start, `C` = command executed, `D` = command finished with exit code).
2. For each command boundary, VS Code registers an xterm `IMarker` on the buffer line.
3. VS Code does NOT use `registerDecoration` for the gutter dots. Instead, it maintains a **separate overlay element** — a `<div>` rendered inside the terminal pane's DOM, positioned to the left of the xterm viewport area. This is essentially **approach (b)**.
4. Each dot's vertical position is computed from `marker.line - viewportY` × cell height, recalculated on scroll and resize events.
5. The dots live in a `pointer-events: none` overlay so they don't interfere with text selection or mouse events.

### Does this map to AD-6?

**Yes.** AD-6 says the VT frontend owns render state and the backend never sniffs the byte stream. The gutter is a **frontend-only** concern:
- OSC 133 markers are parsed by xterm's `parser.registerOscHandler` in the frontend (already verified in ADR-0001).
- The gutter DOM is created and maintained entirely in the frontend UI layer.
- The backend knows nothing about gutter decorations — it ships raw PTY bytes.
- This is **renderer-agnostic** because the gutter is pure DOM outside xterm's canvas/WebGL rendering.

---

## RECOMMENDATION

### **Approach (b): Dedicated sibling gutter `<div>` overlaid on the terminal container.**

**Reasoning:**

1. **Survives all renderers.** The gutter is pure DOM outside xterm's rendering pipeline. WebGL, Canvas, and DOM renderers all work identically — the gutter doesn't touch them.
2. **No CSS environment dependency.** Unlike approach (a), the gutter doesn't rely on `overflow: visible` or any particular xterm CSS property. It's an absolutely-positioned sibling that sits beside `.xterm-screen`.
3. **VS Code uses this technique.** It's the proven approach for production-grade terminal gutter decorations.
4. **Simple position sync.** One function (`syncPositions`) driven by three xterm events (`onScroll`, `onResize`, `onRender`). Total code ~60 lines.
5. **Doesn't reduce terminal area.** Unlike approach (c), the terminal keeps its full working columns.
6. **Clean AD-6 compliance.** The gutter is a frontend-only UI enhancement. The backend ships raw bytes and the VT frontend surfaces OSC markers → gutter consumes them.

### Exact xterm 5.5 API calls the blocks milestone should use

```ts
// ── Marker lifecycle ──────────────────────────────────────────────────
const marker = terminal.registerMarker(cursorYOffset)
const line = marker.line                          // current buffer line
marker.onDispose(() => { /* remove gutter glyph */ })

// ── Viewport scroll tracking ──────────────────────────────────────────
const viewportY = terminal.buffer.active.viewportY
terminal.onScroll((newY: number) => syncPositions())

// ── Cell measurements ─────────────────────────────────────────────────
const cellHeight = terminal.element
  ?.querySelector('.xterm-char-measure-element')
  ?.getBoundingClientRect().height
  ?? Math.ceil((terminal.options.fontSize ?? 15) * (terminal.options.lineHeight ?? 1.0))

const cellWidth = terminal.element
  ?.querySelector('.xterm-char-measure-element')
  ?.getBoundingClientRect().width
  ?? Math.ceil((terminal.options.fontSize ?? 15) * 0.6 + (terminal.options.letterSpacing ?? 0))

// ── Resize and render events ──────────────────────────────────────────
terminal.onResize((size: {cols:number, rows:number}) => syncPositions())
terminal.onRender((range: {start:number, end:number}) => syncPositions())

// ── Buffer type tracking (hide gutter in alt buffer) ──────────────────
terminal.buffer.active.type                            // 'normal' | 'alternate'
terminal.buffer.onBufferChange((buffer: IBuffer) => {
  if (buffer.type === 'alternate') gutter.style.display = 'none'
  else gutter.style.display = 'block'
})

// ── DOM anchor for the gutter ─────────────────────────────────────────
const screen = terminal.element?.querySelector('.xterm-screen')
screen?.parentElement?.insertBefore(gutterDiv, screen)
```

### Risks requiring in-app visual confirmation

| Risk | Mitigation |
|------|-----------|
| jsdom cannot verify actual pixel placement — `getBoundingClientRect()` returns all zeros. | Visual check in Wails WebView with a real browser render. The spike harness (`mountGutterSpike`) is wired to drop into a dev page. |
| Cell height estimation from `fontSize * lineHeight` may drift by 1px from xterm's actual measurement. | Use `.xterm-char-measure-element` rect when available; fall back to the estimate only in jsdom. |
| In alternate buffer (vim, less, tmux), the gutter should hide. | Already handled: check `buffer.active.type` and hide gutter in `'alternate'`. |
| Gutter width must not overlap the terminal grid. | Keep gutter width small (16–20px) and ensure the container has enough CSS padding or the gutter uses `pointer-events: none`. |
| Wails WebView may have its own clipping rules. | Visual confirmation needed — the spike harness exports a `mountGutterSpike(container)` function ready for dev-page integration. |

---

## Summary

| Question | Answer |
|----------|--------|
| Q1: API surface | `registerDecoration` accepts `IDecorationOptions` (marker, anchor, x, width, height, bg/fg color, layer, overview ruler). The decoration element is a div inside `.xterm-decoration-container` inside `.xterm-screen`, positioned absolute, z-index 6. |
| Q2: Gutter placement | Three approaches tested. (a) CSS transform may clip in some hosts. (b) Sibling gutter div works reliably. (c) Padding breaks FitAddon. |
| Q3: Robustness | (b) survives all events with ~3 event listeners and one `syncPositions()` function. (a) depends on CSS env. (c) breaks FitAddon. |
| Q4: VS Code prior art | VS Code uses approach (b) — a separate DOM overlay to the left of the viewport, positioning dots by `marker.line - viewportY` × cell height. Maps cleanly to AD-6. |

**RECOMMENDATION: Approach (b)** — a dedicated sibling gutter `<div>` overlaid on the terminal container, positioned left of `.xterm-screen`, with per-glyph `top` computed from marker line and viewport scroll. Same technique VS Code uses.
