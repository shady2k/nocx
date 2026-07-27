# ADR-0014 — Component kit foundation: platform-first, per-primitive

- **Status:** Accepted
- **Date:** 2026-07-27
- **Related:** ADR-0012 (SolidJS as the application UI layer), ADR-0013 (plain CSS with
  semantic custom properties), design spec `2026-07-27-ui-shell-kit-and-theming-design.md`
  §4 (component kit), `.internal/measure/2026-07-27-kit-foundation-measurement.md`
  (corrected per-primitive measurement), `.internal/specs/2026-07-27-kobalte-spike-report.md`
  (superseded first spike, with correction header), beads `nocx-vxqj` (component kit epic)
- **Fills a gap:** ADR-0012 settled the framework but deferred the kit (§3, "A large prebuilt
  visual component suite" was rejected but the positive choice was not made). The design
  spec was explicit that this required a separate ADR gated on a measurement.

## Context

The component kit was the last unsettled question from the SolidJS adoption. ADR-0012
rejected "a large prebuilt visual component suite" but left the affirmative choice —
library or hand-rolled, which library, which primitives — for a measurement gate. The
design spec (§4) framed the hypothesis as per-primitive, platform-first, but left the
decision open until a spike reported measured gzip deltas against the real production entry
with bytes attributed by package.

### The spike and its correction

Two measurements were run. The first (`nocx-vxqj.3`, first dispatch) built primitives
**cumulatively with Select first**, so its headline "~34 KB shared core" was Select's
dependency closure — `@internationalized/number`, collection machinery and full floating-ui
stack — charged to everything built after it. Its WebKit portal results were sound; its
bundle conclusion was not.

The corrected second measurement built each primitive **independently** against the real
production entry (a Vite production build of `frontend/`, 623 KB raw / 163 KB gzip):

| Primitive                        | ΔGzip    | Notable dependencies                                                         |
| -------------------------------- | -------- | ---------------------------------------------------------------------------- |
| **Dialog**                       | +11.6 KB | Core infrastructure only (portal, focus scope, escape key, interact-outside) |
| **Tooltip**                      | +17.4 KB | Core + floating-ui collision detection                                       |
| **Popover**                      | +20.9 KB | Core + floating-ui + more positioning                                        |
| **DPT (Dialog+Popover+Tooltip)** | +23.7 KB | Shared infrastructure paid once                                              |
| Select                           | +31.7 KB | Core + floating-ui + collection + `@internationalized/number`                |
| ContextMenu                      | +30.5 KB | Core + floating-ui + menu machinery                                          |
| Combobox                         | +33.6 KB | Core + floating-ui + collection + `@internationalized/number`                |
| CorvuDialog                      | +4.1 KB  | Solid-native, no separate focus-scope or portal system                       |

For comparison: a custom overlay core (portal root, overlay stack, Escape ownership,
interact-outside, focus return) is estimated at **3–5 KB gzip**, and `@floating-ui/dom` at
**7–10 KB gzip** is replacement cost, not overhead — any positioned primitive needs it.

### The budget

ADR-0012 sets a budget of **25–35 KB gzip net** for framework, store and initial component
kit combined. The Solid migration already spent **+7 KB** of that, leaving **18–28 KB**.

Kobalte's DPT at +23.7 KB fits within the upper bound (28 KB) but exceeds the lower bound
(18 KB). The budget alone does not decide this question.

### Consumer evidence (verified 2026-07-27)

The decisive fact is what primitives this codebase actually uses:

| Primitive               | Production consumers                                                                                                                                                                                                                                                                                                | Verified by                                                                                                 |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| **Dialog**              | 3 `confirm()` calls (`connections.tsx:166`, `:178`, `terminal-content.ts:460`). No `<dialog>` element, no `HTMLDialogElement`, no `showModal()` anywhere in production code.                                                                                                                                        | `grep -rn 'confirm\|<dialog\|HTMLDialogElement\|showModal' frontend/src --include='*.ts' --include='*.tsx'` |
| **Select**              | 1 native `<select>` in `settings.tsx:588`; >2 kit `<Select>` wrapper usages in `connections.tsx:330,437`. The kit's `ui/select.tsx` exists and is consumed.                                                                                                                                                         | `grep -rn '<select\|HTMLSelectElement' frontend/src --include='*.ts' --include='*.tsx'`                     |
| **Tooltip**             | ~8 sites using the native `title` attribute for tooltips (button labels, data-class descriptions, sidebar item titles), plus component-prop passthrough in `ui/button.tsx`. None need rich content.                                                                                                                 | `grep -rn 'title=' frontend/src --include='*.tsx'`                                                          |
| **Popover**             | **0.** No imports, no usage. `grep() -rn 'popover' -i` returns zero match in production code.                                                                                                                                                                                                                       | `grep -rn 'popover\|Popover' frontend/src --include='*.ts' --include='*.tsx'`                               |
| **Menu / context menu** | **0** as a component. The word `contextmenu` appears only as a CSS property (`--default-contextmenu: hide` at `style.css:899`) and as a right-click event handler on the terminal surface (`terminal-content.ts:469`). Neither is a menu primitive. No `Menu`, `ContextMenu` or `@kobalte/*` imports in production. | `grep -rn 'Menu\|ContextMenu\|contextmenu\|@kobalte' frontend/src --include='*.ts' --include='*.tsx'`       |
| **Combobox**            | **0.** The word appears only in tests as the ARIA role of native `<select>` elements (`screen.getByRole('combobox')`). No Combobox primitive or consumer.                                                                                                                                                           | `grep -rn 'combobox\|Combobox' frontend/src --include='*.ts' --include='*.tsx'`                             |

### Why Kobalte fits the budget and is still declined

The decision was made by consumers, not by bytes.

Kobalte's DPT at +23.7 KB gzip would fit the remaining 18–28 KB budget. It is declined
because:

1. **The primitives a library is genuinely better at — Popover, Menu, Combobox — have zero
   consumers in this codebase.** Paying for floating-ui, ARIA wiring, focus management
   and collection infrastructure for primitives nobody uses is not how this budget gets
   spent.

2. **The primitives we do need are the two the platform already implements well.** Native
   `<dialog>` and native `<select>` cost nothing and are better at accessibility than
   anything we or a library would write — they inherit platform keyboard handling, form
   semantics, typeahead and assistive-technology integration by default.

3. **The one primitive Kobalte would have been most useful for, Select, is also its most
   expensive at +31.7 KB.** Adding Select alone would consume the entire remaining budget
   (18–28 KB) at its upper bound.

## Decision

**No component library. The kit is nocx-owned, and each primitive is implemented by the
cheapest correct means — which today is the platform.**

| Primitive                                  | Implementation                                                                    | Cost | Why                                                                                                                                                         |
| ------------------------------------------ | --------------------------------------------------------------------------------- | ---- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Dialog**                                 | Native `<dialog>` + `showModal()`, behind a kit wrapper                           | 0 KB | Available at both declared floors (WebKitGTK 2.40, Safari 16.2); brings top-layer rendering, background inertness, Escape/cancel and native focus treatment |
| **Select**                                 | Native `<select>`, `appearance: none` on the closed control, behind a kit wrapper | 0 KB | Keyboard operation, platform accessibility, form semantics and typeahead for free                                                                           |
| **Tooltip**                                | The native `title` attribute, as today                                            | 0 KB | ~8 call sites already use it and none needs rich content                                                                                                    |
| **Popover, Menu / context menu, Combobox** | **Not built**                                                                     | —    | No consumer exists                                                                                                                                          |

### What we write ourselves

A small overlay core shared across whatever custom overlays we eventually build:

- **Portal root** — a single `document.body`-level element owned by the kit.
- **Overlay stack** — so Escape closes only the topmost overlay, and interact-outside
  detection targets the correct layer.
- **Focus return** — return focus to the invoker on close, including xterm's hidden
  textarea (which is not a normal focusable element).
- **Wails drag-region guard** — on mousedown over an overlay, check whether the target
  descends from a `--wails-draggable: drag` ancestor; if so, let Wails handle it.

Estimated size: **3–5 KB gzip**. (The `@floating-ui/dom` cost, when a positioned overlay
is eventually needed, is **7–10 KB gzip** and is replacement cost, not overhead — any
positioned primitive needs anchor measurement, flip/shift, scroll-parent tracking and
disconnect cleanup.)

The `<dialog>` wrapper's own policy:

- **Initial focus** — the first focusable element inside the dialog receives focus
  (native default); `autofocus` attribute overrides when specified.
- **Nesting** — a stack of open dialogs; only the topmost is interactive. Native
  `<dialog>` handles this partly but explicit stack management prevents edge cases.
- **`::backdrop` theming** — styled via ADR-0013's token variables.
- **Wails drag-region guard** — before the dialog activates, check mousedown targets for
  `--wails-draggable: drag` ancestry (applies to whatever we build, library or not).

### The first consumer: four dialog call sites

Four unstyled system dialog calls ship today, and they are the concrete visible reason
the Dialog wrapper exists:

| File                               | Line | Call                  | Text                                          |
| ---------------------------------- | ---- | --------------------- | --------------------------------------------- |
| `frontend/src/connections.tsx`     | 166  | bare `confirm(...)`   | `\`Delete "${profile.name}"?\``               |
| `frontend/src/connections.tsx`     | 178  | bare `confirm(...)`   | `\`Delete credential "${credential.name}"?\`` |
| `frontend/src/terminal-content.ts` | 460  | `window.confirm(...)` | `'Paste multi-line text?'`                    |
| `frontend/src/settings.tsx`        | 610  | bare `prompt(...)`    | `'Enter new value for "' + decl.label + '":'` |

A system confirm or prompt dialog in the middle of a dark custom-styled application is the
first thing the Dialog wrapper replaces.

### The guard

An ESLint rule rejects raw interactive elements (`<button>`, `<select>`, `<textarea>`,
`<input type=checkbox|radio>`) and direct `@kobalte/*` imports in application surfaces.
`frontend/src/ui/` is exactly where native implementation details are allowed to live, by
**explicit path exemption** rather than surface-wide waiver. Terminal-owned files
(`terminal-content.ts`, `tabs.ts`, `renderers/`, `scrollback/`) are also exempt; their
controls are inside the xterm boundary, not application UI.

The guard ships with an enumerated baseline allowlist keyed by file and control (matching
the surfaces that still hold raw controls at migration time). This list may only shrink, is
asserted to shrink by CI, and must be empty before `nocx-vxqj` closes. Whole-surface
exemptions are not allowed.

## Rationale

### Why the platform, rather than Kobalte or Corvu

The library question was decided by consumers, not by bytes. Kobalte's DPT at +23.7 KB
would have fitted the budget. It is declined because:

- **Popover, Menu and Combobox have no consumers.** Verified by grep across every `.ts` and
  `.tsx` file in `frontend/src/`: zero imports of any Popover, Menu or Combobox component;
  zero occurrences of `popover` or `combobox` outside of CSS properties and test ARIA roles.
- **Dialog and Select, which do have consumers, are the two primitives the platform already
  implements well.** Native `<dialog>` and `<select>` cost nothing and inherit platform
  accessibility, keyboard handling and form semantics. A library would be paying to replace
  what the platform gives for free.
- **Kobalte's Select — the one primitive we actually need most — is its most expensive at
  +31.7 KB.** Adding Select alone would consume the entire remaining budget at its upper
  bound, leaving nothing for the overlay core, focus return, or the Wails drag-region guard
  that would be needed anyway.

### Why the Kobalte measurement matters even though we declined

The measurement established a baseline that makes a future library adoption **cheaper to
evaluate, not more expensive**:

- DPT at +23.7 KB — known.
- Corvu Dialog at +4.1 KB — known.
- `@floating-ui/dom` at ~7–10 KB — known to be replacement cost, not overhead.

When a real consumer for a positioned overlay arrives, these numbers are on file and the
decision is a delta rather than a new spike.

### Why we favour platform `<select>` despite the styling limit

Native `<select>`'s open popup is platform-owned and cannot be styled to match the
application. That is a **deliberate trade**: accessibility and zero bytes against
pixel-identical popups. The owner can reverse it — at which point it becomes a custom
listbox with its own bead and its own measured cost. For settings choices (theme selector,
font size, credential picker), the platform popup is acceptable.

### Why the HTML Popover API is not foundational

The HTML Popover API (`popover` attribute, `:popover-open` pseudo-class) is Safari 17+ /
macOS 14+ / WebKitGTK ~2.42+ — **above both declared floors** (ADR-0013 §3: macOS 13.1 /
Safari 16.2, WebKitGTK 2.40). It cannot be foundational without either raising the floor
or maintaining two overlapping implementations.

## Consequences

### Positive

- **Zero KB** for the three primitives we actually ship (Dialog, Select, Tooltip). The
  entire remaining budget stays available for what matters: a small overlay core, focus
  return to xterm's hidden textarea, the Wails drag-region guard, and the things the kit
  genuinely needs to write (Button, TextField, Checkbox, etc.).
- **Platform accessibility for free.** Native `<dialog>` and `<select>` inherit the OS's
  keyboard handling, assistive-technology APIs and form semantics. A library or hand-rolled
  replacement would need to replicate all of it, and would get part of it wrong on the
  first pass.
- **One nocx vocabulary, one replacement point.** Surfaces import from `ui/`, never from a
  library. A later change of implementation behind any primitive is a change in `ui/`
  rather than a change everywhere.

### Negative

- **Native `<select>` popup is unstylable.** The open listbox is rendered by the OS or
  browser engine, not by our CSS. For settings with a small number of options (font size,
  theme) this is acceptable; for anything that needs a styled listbox with custom items, a
  later custom implementation is required.
- **The overlay core must be written, not bought.** Portal root, overlay stack, focus
  return to xterm textarea, interact-outside detection and Wails drag-region guard are
  ~300–500 lines of Solid code plus tests. Measurement of a real implementation may revise
  the 3–5 KB gzip estimate.
- **The Wails drag-interaction verification has not run.** The build environment has
  webkit2gtk-4.1; Wails v2 needs 4.0. `--wails-draggable` is read by a native mousedown
  hook that cannot be observed in Playwright. This applies to whatever we build, library or
  not.
- **The xterm focus-return path is untested in a real Wails build.** Returning focus to
  xterm's hidden textarea after a native `<dialog>` closes may require a workaround
  (`requestAnimationFrame` + `.focus()` on a 1×1 invisible input). This is
  **high-severity, testable-only-in-production** — it did not manifest in the Playwright
  run because Playwright's focus model differs from WebKitGTK's.

## Revisit when

A real consumer needs a collision-positioned overlay — Popover (dropdown menu from a
button), Menu (context menu on a tree item), or Combobox (editable autocomplete in a form).

At that point the numbers are already on file:

- Kobalte DPT at **23.7 KB gzip** (pre-built, tested, ARIA-wired)
- Corvu Dialog at **4.1 KB gzip** (Solid-native dialog without Kobalte's weight)
- `@floating-ui/dom` at **~7–10 KB gzip** (replacement cost, not overhead — any positioned
  overlay needs anchor measurement, flip/shift, scroll-parent tracking and disconnect
  cleanup, and CSS Anchor Positioning is unavailable at our floors)

The revisit evaluates whether a library's integration cost (bundle, API surface,
maintenance gap) is worth paying now that a concrete consumer exists, against the known
cost of writing the positioned overlay on the small custom core + floating-ui.

Also revisit when a styled custom listbox is genuinely required (i.e., when the native
`<select>` popup's platform-owned appearance is a product blocker rather than an accepted
trade). Kobalte Select at +31.7 KB is the measured benchmark.
