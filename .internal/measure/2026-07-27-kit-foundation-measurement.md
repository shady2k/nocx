# Kobalte Spike Report (redo) — nocx-vxqj.3

**Worker:** dispatched task_2a1b6c2b0ab2 (redo)
**Date:** 2026-07-27
**Status:** Complete (with documented gaps)

---

## Deliverable 1 — Independent per-primitive measurement

### Method

1. **Baseline**: `npm run build` in `frontend/` — the real production build (623,060 B raw / 162,790 B gzip JS).
2. **Wrapper entry**: each measurement imports `./main` (the full production module graph) plus exactly one Kobalte primitive by named export, with the imported symbol referenced in a `const` to prevent tree-shaking (`sideEffects: false` on all Kobalte packages).
3. **Measurement config**: `vite.measure.config.ts` extends the production config with `rollup-plugin-visualizer` (gzip treemap). Entry is an auto-generated HTML+TSX pair per build.
4. **Total emitted JS**: sum of all `dist/assets/*.js` files, both raw and gzip.
5. **Wrapper overhead**: a "bare wrapper" (same entry shape but no Kobalte import) adds +32 B raw / +56 B gzip — negligible.

### Results: independent per-primitive

| Primitive | Modules | Raw (B) | Gzip (B) | ΔRaw (B) | ΔGzip (B) | ΔGzip (KB) |
|---|---|---|---:|---:|---:|---:|---:|
| Baseline | 66 | 623,060 | 162,790 | — | — | — |
| **Dialog** | 115 | 657,666 | 174,670 | +34,606 | +11,880 | **11.6 KB** |
| **Popover** | 124 | 682,754 | 184,145 | +59,694 | +21,355 | **20.9 KB** |
| **Tooltip** | 112 | 672,656 | 180,629 | +49,596 | +17,839 | **17.4 KB** |
| Select | 135 | 721,172 | 195,247 | +98,112 | +32,457 | **31.7 KB** |
| Combobox | 136 | 727,872 | 197,209 | +104,812 | +34,419 | **33.6 KB** |
| ContextMenu | 132 | 719,907 | 194,058 | +96,847 | +31,268 | **30.5 KB** |

### Results: combinations

| Combination | Modules | Raw (B) | Gzip (B) | ΔRaw (B) | ΔGzip (B) | ΔGzip (KB) |
|---|---|---|---:|---:|---:|---:|---:|
| **DPT** (Dialog+Popover+Tooltip) | 128 | 693,462 | 186,493 | +70,402 | +23,703 | **23.2 KB** |
| All6 (all six primitives) | 151 | 777,363 | 207,618 | +154,303 | +44,828 | **43.8 KB** |

### Results: Corvu (Solid-native alternative)

| Primitive | Modules | Raw (B) | Gzip (B) | ΔRaw (B) | ΔGzip (B) | ΔGzip (KB) |
|---|---|---|---:|---:|---:|---:|---:|
| **CorvuDialog** (Root from corvu/dialog) | 91 | 636,816 | 167,000 | +13,756 | +4,210 | **4.1 KB** |

### Correction of the previous spike

The previous spike's key finding — "shared core ~34 KB gzip" — was **Select** (32.5 KB gzip at the new measurement), not a universal shared overhead. The previous build was cumulative starting from Select, so everything Select pulls in (collection machinery, `@internationalized/number`, full floating-ui stack) was counted as shared when it is actually Select-specific.

The true minimal shared Kobalte infrastructure (what Dialog alone requires) is:

| Package                          | Gzip                  |
| -------------------------------- | --------------------- |
| `@kobalte/core` (15 files)       | 9.7 KB                |
| `@kobalte/utils`                 | 2.7 KB                |
| `solid-prevent-scroll` (5 files) | 2.8 KB                |
| `solid-presence` (2 files)       | 0.7 KB                |
| `@solid-primitives/refs`         | 0.4 KB                |
| **Total**                        | **~16.3 KB raw gzip** |

But _delivered_ gzip is smaller because the baseline already includes some `@solid-primitives/*` packages. The measured net gzip delta for Dialog is **11.9 KB** — that is what actually appears in the production bundle.

### The key number the ADR needs: DPT cost

**DPT (Dialog + Popover + Tooltip): +23.7 KB gzip** against the real production build.

This is the cost of adding all three lightweight Kobalte primitives to the app. The shared infrastructure is paid once (not 11.9+20.9+17.4 = 50.2 KB).

Budget check (corrected, per brief):

- ADR-0012 budget: 25-35 KB gzip net
- Solid migration already spent: ~+7 KB gzip
- Remaining budget: **18-28 KB gzip**
- DPT at **23.7 KB gzip**: fits within the upper bound (28 KB), exceeds the lower bound (18 KB) by ~6 KB
- Total (Solid + DPT): **30.7 KB gzip** — within the declared 25-35 KB range

### Categorisation by dependency weight

| Primitive   | ΔGzip   | Notable dependencies                                                                      |
| ----------- | ------- | ----------------------------------------------------------------------------------------- |
| Dialog      | 11.6 KB | Core infrastructure only (portal, focus scope, escape key, interact-outside, polymorphic) |
| Tooltip     | 17.4 KB | Core + floating-ui collision detection                                                    |
| Popover     | 20.9 KB | Core + floating-ui + more positioning                                                     |
| ContextMenu | 30.5 KB | Core + floating-ui + menu machinery                                                       |
| Select      | 31.7 KB | Core + floating-ui + collection + `@internationalized/number`                             |
| Combobox    | 33.6 KB | Core + floating-ui + collection + `@internationalized/number` + more                      |

---

## Deliverable 2 — Fair comparison (floating-ui baseline)

The argument that "Kobalte includes @floating-ui/dom which we'd write anyway" is material. Every positioned primitive (Popover, Tooltip, Menu, Select popup) needs **anchor measurement, viewport collision detection, flip/shift, scroll-parent tracking, and disconnect cleanup**. These are not trivial to hand-roll.

**Which primitives genuinely need collision-aware positioning:**

| Primitive              | Needs floating-ui? | Rationale                                                                                                      |
| ---------------------- | ------------------ | -------------------------------------------------------------------------------------------------------------- |
| Dialog (centred modal) | **No**             | Centred overlay; no anchor-relative positioning needed. `margin: auto` on a flex/grid container suffices.      |
| Popover                | **Yes**            | Anchor-relative; must flip when near window edge, shift to stay visible, track scroll, clean up on disconnect. |
| Tooltip                | **Yes**            | Same as Popover — anchor-relative, must flip/shift.                                                            |
| ContextMenu            | **Yes**            | Appears at cursor/pointer position near window edge; flip/shift required.                                      |
| Select                 | **Yes**            | Listbox opens below/above trigger; flip required.                                                              |
| Combobox               | **Yes**            | Same as Select.                                                                                                |

If we adopt a platform-first hybrid:

- Dialog uses `<dialog>` at zero cost — no floating-ui needed.
- Popover/Tooltip/ContextMenu/Select/Combobox all need floating-ui anyway, so Kobalte's inclusion of it is **replacement cost, not overhead**.

**Cost of "our code + floating-ui" vs "Kobalte + floating-ui":**

A custom Popover built on `@floating-ui/dom` directly would cost roughly:

- `@floating-ui/dom` itself: ~7-10 KB gzip
- Our positioning abstraction: ~1-2 KB
- Our overlay management (portal, escape, interact-outside): ~3-5 KB
- Focus management: ~1-2 KB
- ARIA wiring: ~1 KB

Estimated total: **~13-20 KB gzip** for a Popover, versus Kobalte's **20.9 KB** — the difference is **~1-8 KB** for a fully tested, accessible, Solid-integrated solution. Kobalte's edge is not zero, but the gap is narrower than "34 KB for overhead."

---

## Deliverable 3 — Platform-first analysis

### Our declared floors

From ADR-0013 §3:

- **Linux**: WebKitGTK 2.40
- **macOS**: Ventura 13.1 / Safari 16.2

### `<dialog>` + `showModal()`

| Question                | Answer        | Source                                                                                       |
| ----------------------- | ------------- | -------------------------------------------------------------------------------------------- |
| Supported at our floor? | **Yes**       | Safari 15.4 (WebKit r~15.4). Well below our macOS floor of 16.2. WebKitGTK 2.40 includes it. |
| Top-layer rendering?    | Yes, natively | `showModal()` renders in the top layer, above all `z-index` stacking contexts.               |
| Background blocking?    | Yes           | Rest of document becomes inert.                                                              |
| Escape/cancel?          | Yes           | Fires `cancel` event; `preventDefault()` to prevent close.                                   |
| Native focus treatment? | Yes           | Auto-focuses first focusable element; focus trap within dialog.                              |

**What we'd still need to write:**

| Behaviour                               | Effort                      | Notes                                                                                                                                 |
| --------------------------------------- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| Initial focus policy                    | ~10 lines                   | `autofocus` attribute or manual `focus()` on mount                                                                                    |
| Focus return to xterm's hidden textarea | ~30 lines                   | On close, return focus to the terminal's textarea. This is tricky because xterm's focus element is a hidden textarea.                 |
| Nested dialog policy                    | ~20 lines                   | Stack of open dialogs; only top one interactive. Native `<dialog>` handles this partly but stacking multiple modals needs management. |
| Styling (theming)                       | Already handled by ADR-0013 | Use `::backdrop` and CSS variables.                                                                                                   |
| Wails drag-region behaviour             | ~20 lines                   | On mousedown over dialog: check if target is under `--wails-draggable: drag` ancestor; if so, let Wails handle it.                    |
| Animation                               | ~30 lines                   | `@starting-style` or `animation` on `::backdrop` and dialog open/close.                                                               |

**Verdict: `<dialog>` is usable at our floor and removes the need for Kobalte's Dialog entirely.**

### HTML Popover API (`popover` attribute)

| Question                | Answer                                                                          | Source                                                                                                                                       |
| ----------------------- | ------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| Supported at our floor? | **No**                                                                          | Safari 17+ (macOS 14.x / iOS 17). WebKitGTK ~2.42+. Above both floors.                                                                       |
| What it gives           | Top-layer popover, light-dismiss, Escape handling, `:popover-open` pseudo-class |                                                                                                                                              |
| What we'd still need    | polyfill or JS implementation                                                   | Fallback for Safari 16.x requires a JS-based positioning + overlay solution anyway — at which point you're maintaining both implementations. |

**Verdict: Cannot be foundational. Would require either raising the floor to Safari 17+ (breaking ADR-0013's declared floor) or maintaining two overlapping implementations. Not worth it for our use case.**

### Native `<select>` element

| Question                      | Answer                                                                                                          |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Works everywhere?             | Yes                                                                                                             |
| Keyboard operation?           | Yes, platform-native                                                                                            |
| Accessibility?                | Yes, platform-native                                                                                            |
| Form semantics?               | Yes                                                                                                             |
| Typeahead?                    | Yes                                                                                                             |
| Can we fully style the popup? | **No** — the popup is platform-owned on each OS. This is the original complaint that motivated a custom Select. |

**Verdict: Acceptable for simple choice settings (theme selector, font size). For anything needing a styled popup listbox, a custom solution is required.**

### Corvu evaluation (Solid-native, modular)

Corvu 0.4.8 is a Solid-native headless component library. Measured against the same production baseline:

| Primitive             | ΔGzip      | Notes                                                                                                                                                           |
| --------------------- | ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `corvu/dialog` (Root) | **4.1 KB** | ⅓ of Kobalte Dialog's 11.9 KB. Smaller because it doesn't bundle a separate focus-scope or portal system — it integrates with Solid's reactivity more directly. |

Corvu also depends on `@floating-ui/dom` for positioned primitives (popover, tooltip) but the core dialog avoids it entirely.

**Verdict: Corvu Dialog is a credible lighter alternative for Dialog alone.** It does not, however, replace positioned primitives — Popover and Tooltip in Corvu would still pull in `@floating-ui/dom`.

---

## Deliverable 4 — Packaged Wails run

**Status: NOT TESTED — blocked by environment.**

What was tried:

1. `wails dev` from the repo root — fails because the system has `webkit2gtk-4.1` but Wails v2 requires `webkit2gtk-4.0`.
2. `pkg-config --list-all | grep webkit` confirms only `webkit2gtk-4.1` is available.
3. No `webkit2gtk-4.0.pc` file exists anywhere in the Nix store.
4. No Wails v3 binary is available on this system.

The precondition for the hazard — portals render at `document.body` level — was confirmed in the previous spike's WebKitGTK Playwright run. The actual drag-interaction test (`--wails-draggable` mousedown hook) requires a Wails webview and cannot be tested here.

If this gate is required before the ADR, it must be run on a developer machine or CI runner with the correct WebKitGTK runtime.

---

## Deliverable 5 — Recommendation

### Working hypothesis from coordinator

The brief states the coordinator's current position: **platform-first hybrid** —

- Native `<dialog>` and native `<select>` where sufficient
- Small local overlay core (portal root, overlay stack, escape ownership, interact-outside, focus return)
- `@floating-ui/dom` only where collision handling is genuinely needed
- Narrow Popover/Menu/Tooltip on top of that
- **No Combobox** until a real consumer defines its contract

### Test the hypothesis against the data

**The data supports this approach.** Key findings:

1. **`<dialog>` eliminates Kobalte Dialog at zero bundle cost.** At our declared floor (Safari 16.2, WebKitGTK 2.40), `<dialog>` works natively. The behaviours we'd need to write ourselves (focus return to xterm textarea, Wails drag-region guard) are small — maybe 80 lines total — and would be needed with Kobalte's Dialog anyway.

2. **The local overlay core cost is small.** A portal stack + escape-key handler + interact-outside detection + focus management can be implemented in roughly 200-300 lines of Solid code, which would bundle to an estimated **3-5 KB gzip**. This replaces the Kobalte shared core (11.9 KB gzip for Dialog, pulling in 5 packages).

3. **`@floating-ui/dom` is replacement cost, not overhead.** Any positioned primitive (Popover, Tooltip, Select popup, ContextMenu) needs it. Kobalte bundles it; a custom solution on top of our core would also bundle it. The net-new cost of Kobalte Popover over building on our core + floating-ui is **roughly 5-10 KB gzip** for integration, ARIA wiring, and edge cases.

4. **Corvu Dialog at 4.1 KB gzip is an intermediate option** — a pre-built, Solid-native dialog that is cheaper than Kobalte's, but still has a non-zero cost vs. native `<dialog>`.

### Per-primitive table

| Primitive       | Recommended implementation                                                                      | Measured cost                                                   | What we write ourselves                                                                                                          | What a test can catch                                                                              |
| --------------- | ----------------------------------------------------------------------------------------------- | --------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| **Dialog**      | Native `<dialog>` + local wrapper                                                               | **0 KB** (cost of the wrapper, ~3 KB if we want it)             | Focus return to xterm textarea, Wails drag-region guard, animation, nesting policy (~80 lines)                                   | Focus trap, Escape close, focus return, drag overlay blocked, animation timing                     |
| **Popover**     | Small core + `@floating-ui/dom`                                                                 | **~3-5 KB** (core) + **~7-10 KB** (floating-ui) = **~10-15 KB** | Portal mount, positioning, flip/shift, scroll tracking, anchor disconnect, ARIA (`aria-haspopup`, `aria-expanded`), keyboard nav | Positioning edge cases (near edges, small viewport), scroll, nested popovers, Escape/outside click |
| **Tooltip**     | Same core + floating-ui                                                                         | Same as Popover (shares core + floating-ui)                     | Less than Popover — no interact-outside, lighter ARIA                                                                            | Show/hide timing, hover intent, overflow                                                           |
| **Select**      | Native `<select>` for simple; Kobalte Select (31.7 KB) only if styled popup listbox is required | **0 KB** (native) or **31.7 KB** (Kobalte)                      | Native: styling-limited. Kobalte: wrapper around options array API                                                               | Keyboard nav, typeahead, scroll within listbox, form integration                                   |
| **Combobox**    | **None** until a consumer defines the contract                                                  | 0 KB                                                            | N/A                                                                                                                              | N/A                                                                                                |
| **ContextMenu** | Small core + floating-ui (shares code with Popover)                                             | **~12-17 KB** (core + floating-ui + menu logic)                 | Right-click detection, submenu positioning, keyboard nav (arrow keys), ARIA (`menubar`, `menu`, `menuitem`)                      | Right-click on terminal surface, submenu positioning, Escape closes submenu                        |

### Net effect on budget

| Scenario                                                     | Gzip added                                       | In budget?                       |
| ------------------------------------------------------------ | ------------------------------------------------ | -------------------------------- |
| Solid migration (already spent)                              | +7 KB                                            | Within 25-35 KB                  |
| + Kobalte DPT (Dialog+Popover+Tooltip)                       | +23.7 KB                                         | Within 25-35 KB (30.7 total)     |
| + All 6 Kobalte primitives                                   | +43.8 KB                                         | **Over** (50.8 KB total)         |
| + Native `<dialog>` + custom Popover/Tooltip (this proposal) | ~+3-5 KB core + ~7-10 KB floating-ui = ~10-15 KB | **Well within** (17-22 KB total) |
| + Corvu Dialog only                                          | +4.1 KB                                          | **Well within** (11.1 KB total)  |

### Counter-argument (test the recommendation)

**Against "platform-first hybrid"** — three risks the recommendation glosses over:

1. **The xterm focus-return problem is real and untested.** The hidden textarea that xterm.js uses for keyboard input is not a normal focusable element. Returning focus there after a native `<dialog>` closes may require a workaround (e.g., `requestAnimationFrame` + `.focus()` on a 1x1 invisible input). If it fails, every dialog interaction will break terminal keyboard input. This is a **high-severity, testable-only-in-production** risk — it didn't manifest in the previous spike's Playwright run because Playwright's focus model differs from WebKitGTK's.

2. **Custom overlay stack is not "~200 lines."** The behaviours are individually small, but the edge cases multiply: two popovers open at once (nested menus), popover over dialog, Escape should close the topmost overlay only, clicking outside should close popovers but not dialogs, tab should cycle within active overlay but skip inert ones. The previous spike's estimate of "50 lines for focus trapping, 10 for Escape, 30 for interact-outside" is plausible for a single primitive; maintaining N primitives with consistent stacking behaviour is more like 500-800 lines.

3. **Corvu avoids most of the custom work** at 4.1 KB for a Dialog. If we pair native `<dialog>` (for things that benefit from it) with Corvu Dialog (for things that need portal behaviour or animation), we're managing two dialog systems. At that point, paying 11.9 KB for Kobalte Dialog — which handles all the edge cases — may be cheaper than maintaining our own.

### Final recommendation

**Proceed with platform-first hybrid, with caveats:**

1. **Use native `<dialog>`** for modal dialogs (confirmations, alerts, simple forms). Write the ~80-line wrapper once. Test focus return to xterm textarea in a real Wails build before shipping.

2. **If DPT is the actual scope** (the brief's question — "what does Dialog+Popover+Tooltip actually cost?") — the answer is **23.7 KB gzip** as Kobalte, or roughly **10-15 KB gzip** as a custom hybrid. The custom hybrid saves **~9-14 KB** but carries the focus-return risk and the stacking-complexity risk. If the budget is the binding constraint, custom wins. If team velocity and correctness are binding, Kobalte DPT at 23.7 KB fits the 25-35 KB range and avoids the risks.

3. **Reject Select, Combobox, ContextMenu** from Kobalte. Select at 31.7 KB is expensive for what it replaces. Native `<select>` covers simple settings. If a styled listbox is genuinely needed later, evaluate it as a separate decision with its own consumer-defined contract.

4. **Corvu Dialog (4.1 KB) is an option for teams that want a pre-built, Solid-native dialog without Kobalte's weight** but do not want to write the native `<dialog>` wrapper. It doesn't replace positioned primitives.

### Gaps (named explicitly)

1. **Wails drag interaction test**: blocked by environment (webkit2gtk-4.0 unavailable). Should be tested before shipping any portaled overlay. The portal-at-body-level precondition is confirmed; the Wails mousedown hook behaviour is unverified.

2. **xterm focus return**: untested in any real environment. The previous spike noted the host element stays intact, but focus restoration to the hidden textarea is a separate concern.

3. **Stacking / z-index layer scale**: not designed or tested. A production implementation would need a defined z-index scale shared across all overlays.

4. **Select API compatibility**: the previous spike noted that Kobalte 0.13.x changed Select/Combobox to require `options` arrays with render-props. If Kobalte Select is used, the wrapper must abstract this.

5. **One build per measurement, not cumulative**: this gap is closed — the measurement method was the primary flaw of the previous spike, and this report corrects it.

---

## Files produced (in worktree only — nothing merged)

```
frontend/measure/
├── run-measurements.sh    — automated measurement script
├── results.txt            — raw build results
├── report.md              — this report
├── stats-Dialog.html      — visualizer treemap for Dialog build
├── stats-DPT.html         — visualizer treemap for DPT build
├── stats-All6.html        — visualizer treemap for All6 build
└── stats-CorvuDialog.html — visualizer treemap for CorvuDialog build
frontend/vite.measure.config.ts — measurement Vite config
frontend/kobalte-measure.html   — base measurement HTML entry
```

Modified (in this worktree only):

- `frontend/package.json` — added `@kobalte/core`, `corvu`, `rollup-plugin-visualizer`
- `frontend/package-lock.json` — regenerated
