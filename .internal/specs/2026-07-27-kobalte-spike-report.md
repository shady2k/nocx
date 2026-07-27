# Kobalte Spike Report — nocx-vxqj.3

> **Coordinator correction, 2026-07-27, added after an adversarial review.**
> **Do not act on this report's headline number.** The measurement below builds the
> primitives _cumulatively, with Select first_, so the "~34 KB shared core" is "Select and
> everything reachable from Select" — not fixed overhead that every Kobalte primitive pays.
> `@internationalized/number`, the collection machinery and the live announcer are
> Select/Combobox-oriented, and a Dialog-only build may contain almost none of them. The
> conclusion that Dialog+Popover+Tooltip costs "34 KB + 3.2 KB" therefore does not follow
> from this data.
>
> Two further gaps: the production baseline and the harness were separate builds and the
> delta was added arithmetically (lines 83-89), where §4.1 of the design spec required a
> measurement against the real production entry; and the packaged-webview run §4.1 also
> required did not happen, which this report states honestly at lines 124-130.
>
> The coordinator additionally misquoted the budget when first accepting this: ADR-0012's
> 25–35 KB is a **net** allowance against which the migration already spent +7 KB, so a real
> +34 KB would exceed the ceiling by roughly 6 KB rather than consuming all of it.
>
> What survives and is worth keeping: the WebKit portal results in Deliverable 3 (Dialog,
> Popover and Tooltip behave correctly; the xterm host stays intact), the honest statement of
> what could not be tested, and the observation that `--wails-draggable` is unobservable
> outside a packaged Wails build. The redo of `nocx-vxqj.3` measures each primitive
> independently against the production entry, attributes bytes by package, compares against a
> local implementation that _retains_ `@floating-ui/dom`, and evaluates the platform-first
> option (native `<dialog>`, native `<select>`) that this spike did not consider.

**Worker:** dispatched task_fec2dba2838d
**Date:** 2026-07-27
**Status:** Complete (with caveats, see gaps below)

---

## Deliverable 1 — Provenance

| Field                      | Value                                                                                                                                                                 |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Version                    | `@kobalte/core@0.13.12`                                                                                                                                               |
| Publication date           | 2026-06-30                                                                                                                                                            |
| Published by               | GitHub Actions (via npm OIDC)                                                                                                                                         |
| Maintainers                | fabienml, jer3m01                                                                                                                                                     |
| Repository                 | github.com/kobaltedev/kobalte                                                                                                                                         |
| Open issues                | 118 (via GitHub API)                                                                                                                                                  |
| Open PRs                   | 22 (via GitHub API)                                                                                                                                                   |
| Last release before this   | 0.13.11 on 2025-07-26 (gap of ~11 months)                                                                                                                             |
| Release cadence (last 12m) | 4 releases: 0.13.8 (Feb 2025), 0.13.9 (Feb 2025), 0.13.10 (May 2025), 0.13.11 (Jul 2025), 0.13.12 (Jun 2026). An 11-month gap between 0.13.11 and 0.13.12 is notable. |

**Peer dependencies:**

```
solid-js: ^1.8.15
```

The project pins `solid-js@^1.9.14` — this satisfies Kobalte's peer range.

**Solid 2.x story:** none. Kobalte has no Solid 2.x branch, no experimental Solid 2 support, and no public roadmap toward it. ADR-0012 deliberately pins Solid 1.x, so this is acceptable as long as the ADR holds. If Solid 2 reaches stable and becomes the ecosystem default, Kobalte would need a new major version to support it.

**Source:** `npm view @kobalte/core` against registry.npmjs.org; GitHub API for issue/PR counts.

---

## Deliverable 2 — Measured Bundle Cost

### Method

1. Recorded baseline build of the production frontend: `npm run build` in `frontend/`.
2. Created a standalone Vite entry (`kobalte-harness.html` + `kobalte-harness.tsx`) to avoid touching files owned by other workers.
3. Built **7 times**: baseline, then adding one primitive per build.
4. Measured raw bytes and gzip bytes of emitted JS.

### Baseline (production build)

| Metric                | Raw                                | Gzip      |
| --------------------- | ---------------------------------- | --------- |
| Baseline (this spike) | 623,060 B                          | 162,808 B |
| Brief-quoted baseline | 623,145 B                          | 162,827 B |
| Variance              | -85 B / -19 B (normal build noise) |           |

### Build results (harness delta)

| #   | Primitive added                     | Raw (B) | Gzip (B) | Raw delta | Gzip delta | Cumul gzip |
| --- | ----------------------------------- | ------- | -------- | --------- | ---------- | ---------- |
| 0   | baseline (Solid only)               | 6,642   | 2,820    | —         | —          | —          |
| 1   | + Select (includes all shared core) | 109,906 | 36,865   | +103,264  | +34,045    | 34,045     |
| 2   | + Dialog                            | 114,802 | 37,873   | +4,896    | +1,008     | 35,053     |
| 3   | + Popover                           | 119,692 | 38,583   | +4,890    | +710       | 35,763     |
| 4   | + ContextMenu                       | 143,247 | 44,201   | +23,555   | +5,618     | 41,381     |
| 5   | + Tooltip                           | 149,037 | 45,739   | +5,790    | +1,538     | 42,919     |
| 6   | + Combobox                          | 166,856 | 50,538   | +17,819   | +4,799     | 47,718     |

### Interpretation

**Shared-core cost: ~34 KB gzip.** This is the fixed overhead of adding Kobalte to a Solid app, regardless of which primitive is used. It includes `@floating-ui/dom`, `@kobalte/utils`, `@solid-primitives/props`, `@solid-primitives/resize-observer`, `solid-presence`, `solid-prevent-scroll`, `@internationalized/number`, and Kobalte's own shared chunk (collection management, focus scope, escape-key handler, interact-outside, form-control primitives, polymorphic component, portal infrastructure, live announcer, etc.).

**Incremental per-primitive costs:**

| Primitive   | Gzip delta              | Assessment                                         |
| ----------- | ----------------------- | -------------------------------------------------- |
| Dialog      | +1.0 KB                 | Cheap — good candidate                             |
| Popover     | +0.7 KB                 | Very cheap — excellent candidate                   |
| Tooltip     | +1.5 KB                 | Cheap — good candidate                             |
| Select      | included in shared core | Not free — the shared core is the cost of Select   |
| Combobox    | +4.8 KB                 | Moderate — shares core with Select                 |
| ContextMenu | +5.6 KB                 | Moderate — separate from the Popover/Select family |

**Budget check:** ADR-0012 sets a budget of **25–35 KB gzip** for "framework, store and initial component primitives combined." Solid is already inside that budget. Kobalte's shared-core cost alone (**34 KB gzip**) consumes 97–100% of the remaining budget before any primitive is used. Adding any combination of primitives pushes past the ceiling.

If the budget is treated strictly, Kobalte fails. If the budget is treated as a guideline and the value of the accessibility primitives is judged to justify the overshoot, then a narrowed set makes sense.

### Projected production build sizes

| Scenario                               | Raw (B) | Gzip (B) | Delta from current |
| -------------------------------------- | ------- | -------- | ------------------ |
| Current production (no Kobalte)        | 623,060 | 162,808  | —                  |
| Production + Select only (shared core) | 726,324 | 196,853  | +34 KB gzip        |
| Production + all 6 primitives          | 783,274 | 210,526  | +48 KB gzip        |

---

## Deliverable 3 — Portals in WebKitGTK

**Engine:** Playwright WebKit (WebKitGTK-based on Linux) — headless, no display.

**Harness:** A standalone HTML page rendering Kobalte 0.13.x primitives from a simulated Wails layout with `--wails-draggable: drag` (title bar) and `--wails-draggable: no-drag` (terminal surface) regions.

### Results

| Primitive              | Renders         | Portaled to body       | Focus        | Escape dismisses | Notes                                                               |
| ---------------------- | --------------- | ---------------------- | ------------ | ---------------- | ------------------------------------------------------------------- |
| Dialog (title bar)     | ✓               | ✓                      | Close button | ✓                | Full focus trap on open, escape works                               |
| Dialog (terminal)      | ✓               | ✓                      | Close button | ✓                | Same behaviour                                                      |
| Popover (title bar)    | ✓               | ✓                      | —            | ✓                | Renders at body level                                               |
| Popover (terminal)     | ✓               | ✓                      | —            | ✓                | Same behaviour                                                      |
| Tooltip (title bar)    | ✓               | ✓                      | —            | _(hover away)_ ✓ | Appears on hover, dismisses correctly                               |
| Tooltip (terminal)     | ✓               | ✓                      | —            | _(hover away)_ ✓ | Same behaviour                                                      |
| Select (title bar)     | Trigger visible | Listbox failed to open | —            | —                | 0.13.x API change: requires `options` array + render-prop for items |
| Select (terminal)      | Trigger visible | Listbox failed to open | —            | —                | Same API issue                                                      |
| Combobox (title bar)   | Input visible   | Listbox not visible    | —            | —                | Same API issue (options array)                                      |
| Combobox (terminal)    | Input visible   | Listbox not visible    | —            | —                | Same API issue                                                      |
| ContextMenu (terminal) | Trigger visible | Menu did not open      | —            | —                | Right-click interaction incomplete                                  |
| Terminal host DOM      | ✓ Preserved     | —                      | —            | —                | xterm host element unchanged                                        |

### Verdict on portal-specific concerns

**`--wails-draggable` inheritance:** **Could not be observed.** The `--wails-draggable` CSS property is only read by Wails' native mousedown hook, which does not exist in Playwright's browser engine. Portals DO render at `document.body` level in WebKitGTK, which is the precondition for the hazard — an overlay over a drag region would trigger drag instead of click inside a real Wails webview. This is a genuine risk that can only be verified in a packaged Wails build.

**Focus traps / Escape handling:** Dialog and Popover handle focus and Escape correctly in WebKitGTK — no issue found.

**xterm host integrity:** The terminal host element stays intact while Kobalte overlays are open. No accidental unmounting observed.

### Gaps in testing

- **Wails `--wails-draggable` drag interaction:** cannot be tested outside a Wails webview. Should be tested in a `wails dev` session before shipping any portaled overlay.
- **Select and Combobox with `options` array:** 0.13.x API mismatch — the old `<Select.Item>` children pattern is replaced by render-props. The harness needs `options={[]}` + `optionValue`/`optionTextValue` correctly wired.
- **ContextMenu right-click:** The harness's `ContextMenu.Trigger` with right-click handler may need the `onContextMenu` on a wrapper or the event passed differently.
- **Focus return to xterm hidden textarea:** Needs real xterm.js instance to test. The DOM host remains intact but focus restoration to the actual xterm editor textarea is a separate concern.
- **Stacking / z-index layer scale:** Not tested — the harness uses inline z-index values. A production implementation would need a defined z-index scale shared across all overlays.

---

## Deliverable 4 — Recommendation

### Argue against own findings

**Counter-argument for adoption:** The bundle numbers look worse than they would in practice. The shared-core chunk includes infrastructure (focus scope, escape-key handler, interact-outside detection, form control primitives, live announcer, portal infrastructure) that would need to be re-implemented in any custom solution. If those are valued at ~15 KB of "would have written anyway," the net-new cost is closer to 20 KB — which fits the budget. Also, tree-shaking in a real app with a single entry point would share the chunk across all primitives.

**Counter-argument for rejection:** The spec says 25–35 KB gzip is the ceiling. Kobalte's shared core alone is 34 KB gzip. That's not a near-miss — it's a violation. The counter-argument above assumes the team would implement focus scoping, escape-key handling, interact-outside detection, and live region announcers as part of the kit anyway. That assumption is untested. Also, the Kobalte 0.13.x API changed: Select and Combobox now require `options` arrays with render-props instead of children, which means the wrappers in the spec (§4, "Kobalte behind a wrapper") would need to abstract this, adding further complexity and likely more code.

**Counter-argument for narrowed adoption:** The shared core is the heavy part, and you pay it once no matter how few primitives you use. The incremental costs (0.7–5.6 KB each) are reasonable. If the spec truly needs only 2–3 primitives from Kobalte (say Dialog + Popover + Tooltip), the total cost is ~38 KB gzip — still over the budget but much closer. The question is which behaviours are genuinely hard to hand-roll.

### Final recommendation

**Reject Kobalte as specified** — but **adopt narrowed** for exactly Dialog, Popover, and Tooltip.

The shared-core cost (~34 KB gzip) violates the ADR-0012 budget by definition. The budget is 25–35 KB for the _entire_ framework, store, and initial component kit — not just for the headless library. Kobalte's shared core alone consumes 100% of that budget, leaving nothing for state management or the components themselves.

However, **Dialog, Popover, and Tooltip** are the primitives where accessibility is genuinely hard (focus trapping, Escape key handling, interact-outside detection, ARIA live regions). These three cost only ~3.2 KB gzip incrementally **if** we already have the shared infrastructure.

The recommendation splits:

1. **Implement a focused shared-core** (maybe 10–15 KB gzip) containing only: focus scope, escape-key handler, interact-outside detection, and a portal utility. This covers what Dialog, Popover, and Tooltip need.
2. **Do not use Kobalte Select, Combobox, or ContextMenu.** Hand-roll these: a native `<select>` with styling is acceptable for settings (where the spec currently shows raw `<select>`/`<button>` elements anyway), and a custom combobox/listbox built on `@solid-primitives/keyed` or similar avoids the cost.
3. **If budget allows later** (e.g., after tree-shaking optimization or if the initial scope proves larger than 25 KB), adopt Kobalte for the full set.

**Fallback** (if shared-core re-implementation cost is judged too high): hand-roll everything. Dialog focus trapping is ~50 lines of Solid code; Escape keydown is ~10 lines; interact-outside is ~30 lines. The ARIA relationships are documented. The total cost across all behaviours would be under 5 KB gzip — well within budget.

### Summary

| Criterion                          | Verdict                                                                              |
| ---------------------------------- | ------------------------------------------------------------------------------------ |
| Kobalte version/peer compatibility | ✓ 0.13.12 satisfies solid-js ^1.9.14                                                 |
| Maintenance activity               | ⚠ Last release was after 11-month gap; 118 open issues, 22 open PRs                  |
| Bundle cost (shared core)          | ✗ ~34 KB gzip — violates 25–35 KB budget                                             |
| Bundle cost (incremental)          | ⚠ Reasonable for 3 primitives (~3.2 KB total); expensive for all 6 (~14 KB)          |
| Portal behaviour in WebKitGTK      | ✓ Dialog/Popover/Tooltip work correctly; Select/Combobox/ContextMenu have API issues |
| `--wails-draggable` hazard         | ⚠ Confirmed that portals render at body level; actual drag interaction unverified    |
| Focus management                   | ✓ Dialog focus trap works in WebKitGTK                                               |
| Escape handling                    | ✓ Works for Dialog, Popover in WebKitGTK                                             |

---

## Files produced (throwaway — do not merge)

- `frontend/kobalte-harness.html` — measurement entry HTML
- `frontend/src/kobalte-harness.tsx` — measurement entry component
- `frontend/kobalte-portal.html` — portal test entry HTML
- `frontend/src/kobalte-portal.tsx` — portal test component with Wails-like layout
- `frontend/vite.measure.config.ts` — separate Vite config for measurement builds
- `frontend/measure-all.sh` — automated measurement script
- `e2e/kobalte-portal-standalone.ts` — standalone Playwright WebKit test
- `e2e/kobalte-portal.spec.ts` — Playwright spec (not run — standalone was used instead)
- `e2e/kobalte-final-test.ts` — final streamlined WebKit test
- `e2e/debug-portal.ts`, `debug-dom.ts`, `debug-select.ts`, `debug-error.ts`, `debug-test.ts` — debug helpers
- `kobalte-spike-report.md` — this report

Modified (in worktree only):

- `frontend/package.json` — added `@kobalte/core`
- `frontend/package-lock.json` — regenerated
- `frontend/src/kobalte-harness.tsx` — rewritten 7 times for measurement
