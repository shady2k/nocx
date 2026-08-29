# Activity bar right edge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** The activity bar and its panel sit at the window's right edge in both tab placements, so a tab-scoped panel is never upstream of the tab selector.

**Architecture:** Three additive changes land first and leave the product exactly as it is — a `pane` variant on the kit's `ResizeHandle`, and an `--activity-bar-width` token the toast area steps around. The move itself is then one commit that reorders `#body`, flips two borders, puts the panel's handle on its leading edge and rewrites every test that pinned the old geometry. A fourth task adds the browser check nobody had for the toast overlap.

**Tech Stack:** Solid + TypeScript, plain CSS with custom properties, Vitest + `@solidjs/testing-library` for units, Playwright for e2e (`e2e/run-in-container.sh`).

**Spec:** `.internal/specs/2026-08-29-activity-bar-right-edge-design.md`

## Global Constraints

- The kit grows by **variants, never near-duplicates** (`AGENTS.md` → "Before you build a UI component"). `pane` is a prop on `ResizeHandle`, not a second component.
- A surface may **place** a kit component and may never **repaint** it.
- Every existing caller of `ResizeHandle` keeps today's behaviour: the new prop defaults to `'before'`.
- DOM order must equal visual order. Do **not** reach for CSS `order` to paint the rail rightmost while keeping it early in the DOM — that trade is rejected in the spec, §5.
- No commit may leave the suite red. Task 3 is deliberately one commit for that reason.
- The worker runs the unit tests for the files it changed and stops there. `make ci-full`, the containerized jobs and the e2e suite belong to whoever integrates (`AGENTS.md` → "Git authority").
- Every commit subject ends with its bead id.
- `.internal/specs/` and `.internal/plans/` are prettier-checked at pre-commit; run `npx prettier --write` on any markdown you touch.

---

### Task 1: `ResizeHandle` learns which side its pane is on

**Files:**

- Modify: `frontend/src/ui/resize-handle.tsx:34-71` (props), `:120-166` (pointer), `:168-202` (keys), `:39-50` (the `orientation` doc)
- Test: `frontend/src/ui/resize-handle.test.tsx`

**Interfaces:**

- Consumes: nothing — this task is self-contained.
- Produces: `ResizeHandleProps.pane?: 'before' | 'after'`, default `'before'`. Task 3 passes `pane="after"`.

**Acceptance Criteria:**

- With `pane="after"` on a vertical separator, dragging the separator left widens the measured pane and dragging it right narrows it.
- With `pane="after"`, ArrowLeft grows and ArrowRight shrinks; `aria-valuenow` follows the width, not the key.
- With `pane="after"`, ArrowUp still grows and ArrowDown still shrinks — the off-axis keys are not physical and do not invert.
- `Home` and `End` still select `min` and `max` on both sides.
- Every existing test in `resize-handle.test.tsx` passes unchanged, which is what proves the default is today's behaviour.

- [ ] **Step 1: Write the failing tests**

Append these two cases inside the existing `describe('ResizeHandle', ...)` block in `frontend/src/ui/resize-handle.test.tsx`:

```tsx
// ── The pane on the other side (nocx-c5cwl) ────────────────────────────
//
// The sidebar moved to the window's right edge, so its handle is on the
// panel's LEADING edge and the pane it measures is AFTER the separator.
// What inverts is the mapping from a gesture to a width, and only for the
// gestures that actually move the separator.

it('pane="after": dragging the separator left widens the pane after it', () => {
  const onChange = vi.fn()
  const onCommit = vi.fn()
  subject({ value: 240, pane: 'after', onChange, onCommit })
  const sep = screen.getByRole('separator')

  fireEvent.pointerDown(sep, { clientX: 500, pointerId: 1 })
  fireEvent.pointerMove(sep, { clientX: 400, pointerId: 1 })
  expect(onChange).toHaveBeenLastCalledWith(340)
  expect(sep.getAttribute('aria-valuenow')).toBe('340')

  fireEvent.pointerUp(sep, { clientX: 400, pointerId: 1 })
  expect(onCommit).toHaveBeenLastCalledWith(340)
})

it('pane="after": the on-axis keys invert, the off-axis and absolute ones do not', () => {
  const onChange = vi.fn()
  subject({ value: 240, pane: 'after', onChange })
  const sep = screen.getByRole('separator')

  // Physical: the separator follows the arrow, so LEFT grows the pane
  // that is to the right of it.
  fireEvent.keyDown(sep, { key: 'ArrowLeft' })
  expect(onChange).toHaveBeenLastCalledWith(248)
  fireEvent.keyDown(sep, { key: 'ArrowRight' })
  expect(onChange).toHaveBeenLastCalledWith(240)
  expect(sep.getAttribute('aria-valuenow')).toBe('240')

  // Off-axis: Up and Down move nothing on a vertical separator. They are
  // APG's plain "increase / decrease", not a direction on screen, so they
  // mean the same thing on both sides.
  fireEvent.keyDown(sep, { key: 'ArrowUp' })
  expect(onChange).toHaveBeenLastCalledWith(248)
  fireEvent.keyDown(sep, { key: 'ArrowDown' })
  expect(onChange).toHaveBeenLastCalledWith(240)

  // Absolute on both sides.
  fireEvent.keyDown(sep, { key: 'Home' })
  expect(onChange).toHaveBeenLastCalledWith(200)
  fireEvent.keyDown(sep, { key: 'End' })
  expect(onChange).toHaveBeenLastCalledWith(640)
})
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd frontend && npx vitest run src/ui/resize-handle.test.tsx
```

Expected: FAIL. `pane` is not a known prop, so it is ignored — the drag reports `140` (clamped to `200`) instead of `340`, and ArrowLeft reports `232` instead of `248`.

- [ ] **Step 3: Add the prop**

In `frontend/src/ui/resize-handle.tsx`, inside `ResizeHandleProps`, immediately after the `orientation` prop:

```ts
  /**
   * Which side of the separator the pane it MEASURES is on — `'before'`
   * (the default, and every caller until the sidebar moved to the window's
   * right edge) or `'after'`.
   *
   * It decides the mapping from a gesture to a width, and only for the
   * gestures that MOVE THE SEPARATOR: the pointer, and the two arrow keys
   * on this handle's own axis. Those invert. The two off-axis arrows move
   * nothing on screen — on a vertical separator they are APG's plain
   * "Up and Right increase" — so they keep meaning increase and decrease on
   * both sides, and `Home`/`End` are absolute on both.
   *
   * The physical direction of a key never changes: with `'after'`,
   * ArrowRight still moves the separator right, which is why it now
   * SHRINKS the pane and `aria-valuenow` goes down. That is the WAI-ARIA
   * window-splitter pattern — the arrow moves the splitter, the value
   * describes the pane being controlled.
   *
   * A variant rather than a second component, for the same reason as
   * `orientation` above.
   */
  pane?: 'before' | 'after'
```

- [ ] **Step 4: Make the arithmetic side-aware**

Add this beside `positionOf` in `frontend/src/ui/resize-handle.tsx` (just above it):

```ts
/** +1 when the measured pane is before the separator — moving the
 *  separator away from it grows it — and -1 when it is after. The one
 *  place `pane` reaches the arithmetic; everything below is side-blind,
 *  exactly as `positionOf` is the one place `orientation` reaches it. */
const sign = (): number => (props.pane === 'after' ? -1 : 1)
```

Then apply it in the three places that convert a movement into a width:

```ts
// endDrag
const final = clamp(startValue + sign() * (position - startPos))

// onPointerMove
report(startValue + sign() * (positionOf(e) - startPos), false)
```

and in `onKeyDown`, on the two on-axis keys of each orientation only:

```ts
      case 'ArrowRight':
        if (horizontal) return
        next = live + sign() * step
        break
      case 'ArrowLeft':
        if (horizontal) return
        next = live - sign() * step
        break
      case 'ArrowDown':
        next = horizontal ? live + sign() * step : live - step
        break
      case 'ArrowUp':
        next = horizontal ? live - sign() * step : live + step
        break
```

Note which halves carry `sign()`: for a `vertical` separator Left/Right are physical and Up/Down are not; for a `horizontal` one it is the other way round, and Left/Right already return early.

- [ ] **Step 5: Correct the two comments this outdates**

In the same file, the `orientation` doc says `vertical` is "what the sidebar has always been" (`:39-50`) — the sidebar is still vertical, so keep the sentence and drop only the parenthetical claim about the default caller. And extend the growing-key comment above `case 'ArrowRight'` so it names the new degree of freedom:

```ts
// The growing key is the one that points AWAY from the pane being
// measured: right for a left pane, down for a top one — and the
// mirror of each when `pane` says the measured pane is on the other
// side. A horizontal edge deliberately does not answer Left/Right at
// all — a key that moves nothing is better than a key that moves the
// wrong edge.
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
cd frontend && npx vitest run src/ui/resize-handle.test.tsx
```

Expected: PASS, all cases including the pre-existing ones — the default is unchanged.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/ui/resize-handle.tsx frontend/src/ui/resize-handle.test.tsx
git commit -F - <<'EOF'
feat(frontend): ResizeHandle learns which side its pane is on (nocx-c5cwl)

The handle assumed the pane it measures is before the separator: a drag
right and an ArrowRight both grew it. The sidebar is moving to the window's
right edge, where its handle sits on the panel's leading edge and the pane
is after the separator instead.

`pane` decides the mapping from a gesture to a width, and it changes only
the gestures that move the separator — the pointer and the two arrow keys
on the handle's own axis. The off-axis arrows move nothing on screen; on a
vertical separator they are APG's "Up and Right increase", so they keep
meaning increase and decrease on both sides, and Home/End stay absolute.
The physical direction of a key never changes: with `pane='after'`
ArrowRight still moves the separator right, which is why it now shrinks the
pane and aria-valuenow goes down. That is the window-splitter pattern — the
arrow moves the splitter, the value describes the pane.

A prop rather than a second component, for the reason already written into
this file for `orientation`: the capture, the clamping, the commit-once rule
and the idle-gesture suppression are identical on both sides. The default is
`before`, so every existing caller and every existing test is untouched,
which is what the unchanged cases in the suite are there to prove.
EOF
```

---

### Task 2: The rail's width becomes a token, and the toast area steps around it

**Files:**

- Modify: `frontend/src/styles/tokens.css:42-49` (add the token), `frontend/src/style.css:122-131` (`#activitybar` reads it), `frontend/src/styles/components/toast.css:9-20` (offset)
- Test: `frontend/src/ui/toast.test.tsx`

**Interfaces:**

- Consumes: nothing.
- Produces: the CSS custom property `--activity-bar-width` (48px), readable anywhere under `:root`.

**Acceptance Criteria:**

- `--activity-bar-width` is defined once, in `tokens.css`, and `#activitybar` reads it instead of repeating `48px`.
- `.ui-toast-host` offsets its right edge by `--activity-bar-width` in addition to `--space-4`.
- No literal `48px` remains in the `#activitybar` rule.

This task lands **before** the move on purpose: while the rail is still on the left the offset is invisible, so it cannot break anything, and Task 3 then has nothing left to think about.

- [ ] **Step 1: Write the failing test**

Append to `frontend/src/ui/toast.test.tsx`:

```tsx
// ── The notification area clears the activity bar (nocx-c5cwl) ────────────
//
// The host is fixed to the viewport's bottom-right corner. Once the activity
// bar is on the right edge, that corner is the rail's bottom zone — the API
// workbench and Settings buttons — and a `danger` toast is sticky, so it
// would sit on two global actions until dismissed.
//
// Read off the stylesheet SOURCE because jsdom loads no CSS: a
// getComputedStyle assertion here would pass against a stylesheet that says
// anything at all. Same reason sidebar.test.tsx reads its file. The
// behaviour itself is asserted in a real browser by
// e2e/toast-clears-activity-bar.spec.ts.
describe('the notification area clears the activity bar', () => {
  const TOAST_CSS = readFileSync('src/styles/components/toast.css', 'utf8')
  const TOKENS_CSS = readFileSync('src/styles/tokens.css', 'utf8')

  it('offsets its right edge by the rail width rather than hugging the corner', () => {
    expect(TOAST_CSS).toMatch(
      /\.ui-toast-host\s*\{[^}]*right:\s*calc\(var\(--space-4\)\s*\+\s*var\(--activity-bar-width\)\)/,
    )
  })

  it('the rail width is a token, so the two rules cannot drift apart', () => {
    expect(TOKENS_CSS).toMatch(/--activity-bar-width:\s*48px/)
  })
})
```

Add the import at the top of the file, beside the existing ones:

```tsx
import { readFileSync } from 'node:fs'
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd frontend && npx vitest run src/ui/toast.test.tsx
```

Expected: FAIL on both new cases — `toast.css` still says `right: var(--space-4)` and `tokens.css` has no such token.

- [ ] **Step 3: Define the token**

In `frontend/src/styles/tokens.css`, after `--space-12: 48px;` (`:48`):

```css
/* Shell geometry, not spacing — the width of the activity bar, which is a
     fixed rail and not a step on any scale. It is a token because two rules
     need the same number: `#activitybar` itself, and the notification area,
     which is fixed to the viewport corner the rail now occupies and has to
     step around it (nocx-c5cwl). */
--activity-bar-width: 48px;
```

- [ ] **Step 4: Read it from both rules**

In `frontend/src/style.css`, `#activitybar`:

```css
width: var(--activity-bar-width);
```

In `frontend/src/styles/components/toast.css`, `.ui-toast-host`:

```css
/* Past the activity bar, not merely inside the viewport: the rail is at
     the window's right edge and its bottom zone holds global actions, and a
     `danger` toast is sticky. The panel is deliberately NOT cleared — it
     collapses and it resizes, and a toast over it is no worse than a toast
     over a pane. */
right: calc(var(--space-4) + var(--activity-bar-width));
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd frontend && npx vitest run src/ui/toast.test.tsx
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/styles/tokens.css frontend/src/style.css \
        frontend/src/styles/components/toast.css frontend/src/ui/toast.test.tsx
git commit -F - <<'EOF'
feat(frontend): the notification area steps clear of the activity bar (nocx-c5cwl)

The toast host is fixed to the viewport's bottom-right corner, which today
is terminal content and after the activity bar moves is the rail's bottom
zone — the API workbench and Settings. A danger toast is sticky, so it would
sit on two global actions until somebody dismissed it.

Landing this before the move rather than with it: while the rail is still on
the left the offset changes nothing anybody can see, so it cannot break the
move, and the move has one less thing in it.

The 48px was written twice once the offset existed, so it becomes a token.
The panel is deliberately not cleared as well — it collapses and it resizes,
and a toast over it is no worse than a toast over a pane. The rail is the
surface that is always present and always holds global actions.
EOF
```

---

### Task 3: The move

**Files:**

- Modify: `frontend/src/App.tsx:26-43`
- Modify: `frontend/src/style.css:112-149`
- Modify: `frontend/src/sidebar.tsx:200-203`, `:222-244`, `:344-346`
- Modify: `frontend/src/sidebar.test.tsx:296-329`
- Modify: `e2e/sidebar-resize.spec.ts:77-88`, `:183-199`
- Modify: `e2e/vertical-tab-placement.spec.ts:41-71`

**Interfaces:**

- Consumes: `ResizeHandleProps.pane` from Task 1; `--activity-bar-width` from Task 2.
- Produces: the shell invariant every later test asserts — the activity bar's left edge is at or to the right of `#panes`' right edge, in both placements.

**Acceptance Criteria:**

- In both tab placements the activity bar is the rightmost element of `#body` and the sidebar panel is immediately to its left.
- With vertical tabs the strip is the leftmost element and `#panes` keeps non-zero width.
- Dragging the sidebar's handle to the **left** widens the panel; to the right narrows it.
- ArrowRight on the focused handle narrows the panel and every step still persists.
- The `#body` DOM order equals the visual order — no `order` declaration is introduced.

This is one commit. Splitting it leaves the suite red in between: the moment `#sidebar` is to the right of `#panes`, every drag expectation that assumed otherwise is wrong.

- [ ] **Step 1: Reorder the shell**

In `frontend/src/App.tsx`, replace the `#body` children and the comment above `#vertical-tabstrip`:

```tsx
<div id="body">
  {/* The vertical tab strip lists the panes, so it sits next to them,
            at the window's leading edge. */}
  <div id="vertical-tabstrip" />
  <div id="panes" />
  {/* The panel and its rail are LAST, at the window's trailing edge,
            and in both tab placements.

            Not because they are app-level chrome — that was the old reason
            and it was wrong. Three of the six panel views render for
            whatever tab is in front (Files, Ports and Git read
            `activeOrigin` / `activeProfileId`); the other three are
            window-scoped. A window-scoped view is equally at home on either
            edge, and a tab-scoped one placed BEFORE the thing that selects
            the tab is not. So this edge is correct or neutral for all six
            and the leading edge is wrong for three — the asymmetry is the
            reason, not a generalisation about all of them.

            The trade is real and is taken deliberately: traversal now
            reaches the panel's content before the toolbar that chooses it.
            Painting the rail rightmost with `order` while keeping it early
            in the DOM would make DOM order disagree with visual order,
            which is the worse defect. See
            .internal/specs/2026-08-29-activity-bar-right-edge-design.md §5. */}
  <div id="sidebar" />
  <div id="activitybar" />
</div>
```

Update the block comment at the top of the component so the `#tabbar`/`#body` inventory matches.

- [ ] **Step 2: Flip the two borders**

In `frontend/src/style.css`, change the section heading at `:112` to `App body row: panes | sidebar | activity bar`, and in both rules:

```css
#activitybar {
  ...
  border-left: 1px solid var(--color-divider);
}

#sidebar {
  ...
  border-left: 1px solid var(--color-divider);
}
```

In the `#sidebar` doc comment, "the kit ResizeHandle owns the last 6px" becomes "the first 6px": the panel is still a flex row, but the handle is now its leading slot.

- [ ] **Step 3: Move the handle to the leading slot and tell it which side it is on**

In `frontend/src/sidebar.tsx`, `PanelRoot`, put the `ResizeHandle` before `ActiveView` and add the prop:

```tsx
<Show when={activeDesc()}>
  {/* The handle is the flex row's LEADING slot (see #sidebar in
          style.css): a real flex item, never an overlay, so it can neither
          cover the view's scrollbar nor be covered by it. First in the DOM
          because it is first on screen — the panel is at the window's
          trailing edge, so the edge the user drags is its left one. */}
  <Show when={props.resize}>
    <ResizeHandle
      ariaLabel="Resize sidebar"
      pane="after"
      value={width()}
      min={SIDEBAR_WIDTH_MIN}
      max={SIDEBAR_WIDTH_MAX}
      step={SIDEBAR_WIDTH_STEP}
      onChange={(w) => props.resize!.apply(w)}
      onCommit={(w) => props.resize!.apply(w, { persist: true })}
      onDragStateChange={(dragging) => props.resize!.setDragging(dragging)}
    />
  </Show>
  <ActiveView
    desc={activeDesc()!}
    collapsed={() => props.state.sidebar.collapsed}
    getActiveProfileId={props.getActiveProfileId}
    getActiveOrigin={props.getActiveOrigin}
  />
</Show>
```

Also fix the `resize` prop's doc at `:200-203` — "at its trailing edge" becomes "at its leading edge".

- [ ] **Step 4: Correct the false generalisation at `sidebar.tsx:345`**

It reads "Every sidebar view speaks for the machine a terminal tab is on". Three of six do not. Replace the opening sentence:

```ts
// The panel's tab-scoped views — Files, Ports and Git — speak for the
// machine a terminal tab is on; a Settings tab is not a place, so arriving
// on one collapses the panel and the width goes to the settings content.
// The window-scoped views (Notes, Operations, Notifications) have nothing
// to say about a Settings tab either, so the collapse is right for the
// whole panel and not only for the half that reads `activeOrigin`.
```

- [ ] **Step 5: Run the sidebar unit tests to see them fail**

```bash
cd frontend && npx vitest run src/sidebar.test.tsx
```

Expected: FAIL. `a drag resizes the panel live and persists once on release` now reports `200` (240 − 100, clamped) instead of `340`; `keyboard resizing commits each step and clamps at the bounds` steps down from 636 instead of up.

- [ ] **Step 6: Rewrite those two cases to the new truth**

In `frontend/src/sidebar.test.tsx`:

```tsx
it('a drag resizes the panel live and persists once on release', () => {
  const { bar, panel } = mount()
  const persist = vi.fn()
  const ctrl = createSidebarWidthController(panel, 240, persist)
  mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION], undefined, undefined, undefined, ctrl)

  // The panel is at the window's trailing edge, so its handle is on the
  // LEFT and dragging left is what widens it.
  const sep = panel.querySelector('[role="separator"]') as HTMLElement
  fireEvent.pointerDown(sep, { clientX: 200, pointerId: 1 })
  expect(ctrl.isDragging()).toBe(true)
  fireEvent.pointerMove(sep, { clientX: 100, pointerId: 1 })
  expect(panel.style.getPropertyValue('--sidebar-width')).toBe('340px')
  expect(persist).not.toHaveBeenCalled() // still dragging

  fireEvent.pointerUp(sep, { clientX: 100, pointerId: 1 })
  expect(ctrl.isDragging()).toBe(false)
  expect(persist).toHaveBeenCalledWith(340)
})

it('keyboard resizing commits each step and clamps at the bounds', () => {
  const { bar, panel } = mount()
  const persist = vi.fn()
  const ctrl = createSidebarWidthController(panel, 636, persist)
  mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION], undefined, undefined, undefined, ctrl)

  // ArrowLeft moves the separator left, which on this side is the growing
  // direction — the ceiling is what it runs into.
  const sep = panel.querySelector('[role="separator"]') as HTMLElement
  fireEvent.keyDown(sep, { key: 'ArrowLeft' })
  expect(panel.style.getPropertyValue('--sidebar-width')).toBe('640px')
  expect(persist).toHaveBeenLastCalledWith(640)
  fireEvent.keyDown(sep, { key: 'ArrowLeft' }) // already at the ceiling
  expect(persist).toHaveBeenCalledTimes(1)
  fireEvent.keyDown(sep, { key: 'Home' })
  expect(panel.style.getPropertyValue('--sidebar-width')).toBe('200px')
  expect(persist).toHaveBeenLastCalledWith(200)
})
```

- [ ] **Step 7: Run the sidebar unit tests to verify they pass**

```bash
cd frontend && npx vitest run src/sidebar.test.tsx src/ui/resize-handle.test.tsx src/ui/toast.test.tsx
```

Expected: PASS.

- [ ] **Step 8: Flip the e2e drag helper**

In `e2e/sidebar-resize.spec.ts`, the helper's contract stays "drag by `dx` to grow" so every call site is untouched; only the direction it converts that into changes:

```ts
/** Drag the resize handle so the sidebar grows by `dx` pixels (negative
 *  shrinks). The handle is on the panel's LEADING edge — the panel is at the
 *  window's trailing edge — so growing it means moving the pointer LEFT. */
async function dragHandle(page: import('@playwright/test').Page, dx: number): Promise<void> {
  const handle = page.locator(HANDLE)
  const box = await handle.boundingBox()
  if (!box) throw new Error('resize handle has no box')
  const startX = box.x + box.width / 2
  const y = box.y + box.height / 2
  await page.mouse.move(startX, y)
  await page.mouse.down()
  await page.mouse.move(startX - dx, y, { steps: 10 })
  await page.mouse.up()
}
```

- [ ] **Step 9: Rewrite the e2e keyboard expectations**

Same file, the `the separator is keyboard-operable and every step persists` test. ArrowRight now moves the separator right, which narrows the panel:

```ts
await page.locator(HANDLE).focus()
// ArrowRight moves the separator right; the panel is to its right, so
// the panel narrows. The key's physical direction is unchanged — what
// changed is which side the panel is on.
await page.keyboard.press('ArrowRight')
await expect.poll(() => sidebarWidth(page)).toBe(232)
await expect.poll(() => persistedWidth(backend)).toBe(232)

await page.keyboard.press('ArrowRight')
await expect.poll(() => sidebarWidth(page)).toBe(224)

await page.keyboard.press('Home')
await expect.poll(() => sidebarWidth(page)).toBe(200)
```

- [ ] **Step 10: Replace the placement e2e with the new invariant**

In `e2e/vertical-tab-placement.spec.ts`, replace the whole first test:

```ts
test('the activity bar keeps the trailing edge in both placements (nocx-c5cwl)', async ({
  page,
}) => {
  // The invariant, asserted where a user could see it broken: whatever the
  // tab placement, the rail is the last thing across the window and the
  // panes are before it. Checked in BOTH placements because the whole
  // point of the move is that the shell does not rearrange itself when the
  // placement changes.
  for (const placement of ['vertical', 'horizontal'] as const) {
    await switchPlacement(page, placement)

    const barBox = await page.locator('#activitybar').boundingBox()
    const panesBox = await page.locator('#panes').boundingBox()
    expect(barBox, placement).not.toBeNull()
    expect(panesBox, placement).not.toBeNull()

    // The rail is past the panes...
    expect(barBox!.x, placement).toBeGreaterThanOrEqual(panesBox!.x + panesBox!.width - 1)
    // ...and the panes still have room.
    expect(panesBox!.width, placement).toBeGreaterThan(0)

    if (placement === 'vertical') {
      // The strip lists the panes and now owns the leading edge.
      const stripBox = await page.locator('#vertical-tabstrip').boundingBox()
      expect(stripBox, placement).not.toBeNull()
      expect(stripBox!.x + stripBox!.width, placement).toBeLessThanOrEqual(panesBox!.x + 1)
      // Below the drag bar (not at y=0 inside #tabbar).
      expect(stripBox!.y, placement).toBeGreaterThanOrEqual(30)
    }
  }
})
```

- [ ] **Step 11: Format and commit**

```bash
npx prettier --write frontend/src/App.tsx frontend/src/style.css frontend/src/sidebar.tsx \
    frontend/src/sidebar.test.tsx e2e/sidebar-resize.spec.ts e2e/vertical-tab-placement.spec.ts
git add frontend/src/App.tsx frontend/src/style.css frontend/src/sidebar.tsx \
        frontend/src/sidebar.test.tsx e2e/sidebar-resize.spec.ts e2e/vertical-tab-placement.spec.ts
git commit -F - <<'EOF'
feat(frontend): the activity bar and its panel move to the trailing edge (nocx-c5cwl)

With vertical tabs the shell read bar | panel | strip | panes, and three of
the six panel views render for whatever tab is in front. The panel was
downstream of the tab selector and sat upstream of it. Horizontal placement
never had the problem because #tabbar spans the width above everything, so
the strip already dominated.

The reason written into App.tsx is an asymmetry, not the generalisation the
old comment made: a window-scoped view is equally at home on either edge, a
tab-scoped one placed before the tab selector is not, so the trailing edge is
correct or neutral for all six views while the leading edge is wrong for
three. sidebar.tsx said all six speak for the tab in front; three do not, and
that sentence is corrected here too.

One commit rather than several because the moment #sidebar is right of
#panes, every drag expectation that assumed otherwise is wrong — there is no
ordering of smaller commits that keeps the suite green. The handle moves to
the panel's leading slot and takes pane="after", so a drag left widens and
ArrowRight narrows; the two unit cases and the e2e helper follow.

The traversal trade is taken deliberately: a reader now reaches the panel's
content before the toolbar that chooses it. Painting the rail rightmost with
`order` while keeping it early in the DOM would make DOM order disagree with
visual order, which is worse than what it would fix.
EOF
```

---

### Task 4: The browser watches the toast clear the rail

**Files:**

- Create: `e2e/toast-clears-activity-bar.spec.ts`

**Interfaces:**

- Consumes: the shell invariant from Task 3, `--activity-bar-width` from Task 2.
- Produces: nothing.

**Acceptance Criteria:**

- A visible toast's bounding box does not intersect the Settings button's bounding box.
- The test raises a real toast through a user action, not by calling into the app.

Task 2's assertion reads the stylesheet, which cannot fail if the geometry is wrong for some other reason. This is the check that watches a user see it — the overlap was missed in the first place because nothing did.

- [ ] **Step 1: Write the failing test**

Create `e2e/toast-clears-activity-bar.spec.ts`:

```ts
/**
 * e2e: a toast never covers the activity bar's global actions (nocx-c5cwl).
 *
 * The notification area is fixed to the viewport's bottom-right corner, and
 * that corner is now the rail's bottom zone — the API workbench and Settings.
 * A `danger` toast is sticky, so an overlap is not a flicker: it is two
 * global actions unreachable until somebody dismisses it.
 *
 * The toast is raised the way a user raises one — the Git panel's copy
 * affordance, which answers with a confirmation toast — rather than by
 * reaching into the app, because a toast the test injected proves the CSS
 * and not the product.
 */
import { test, expect } from './harness'
import { createRepo, cleanupRepo } from './git-fixture'

const VIEW_GIT = 'button[data-view="git"]'
const BRANCH = '[data-testid="git-branch"]'
const COPY_BRANCH = '[data-testid="git-copy-branch"]'
const TOAST = '.ui-toast'
const SETTINGS = 'button[data-action="settings"]'
const TAB_TITLE = '.nocx-tab-title'

test('a toast does not cover the activity bar (nocx-c5cwl)', async ({ page }) => {
  const repo = createRepo({ file: 'a.txt' })
  try {
    await page.goto('/')
    await expect(page.locator(TAB_TITLE).first()).not.toHaveText('', { timeout: 10_000 })
    await page.keyboard.type(`cd ${repo.root}`)
    await page.keyboard.press('Enter')
    await expect(page.locator(TAB_TITLE).first()).toContainText(repo.basename, { timeout: 20_000 })
    await page.locator(VIEW_GIT).click()
    await expect(page.locator(BRANCH)).toBeVisible({ timeout: 20_000 })

    await page.locator(COPY_BRANCH).click()
    const toast = page.locator(TOAST).first()
    await expect(toast).toBeVisible({ timeout: 10_000 })

    const toastBox = await toast.boundingBox()
    const settingsBox = await page.locator(SETTINGS).boundingBox()
    expect(toastBox).not.toBeNull()
    expect(settingsBox).not.toBeNull()

    // No horizontal overlap: the toast's right edge stops before the rail's
    // left edge. Vertical overlap is expected and fine — both live at the
    // bottom of the window.
    expect(toastBox!.x + toastBox!.width).toBeLessThanOrEqual(settingsBox!.x)
  } finally {
    cleanupRepo(repo)
  }
})
```

- [ ] **Step 2: Prove it catches the defect, by hand-reverting the one line**

Do **not** use `git stash` for this — the stash stack is shared with every
other worktree on this machine. Edit the single declaration back:

```bash
# in frontend/src/styles/components/toast.css, .ui-toast-host:
#   right: calc(var(--space-4) + var(--activity-bar-width));
# temporarily becomes
#   right: var(--space-4);
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/toast-clears-activity-bar.spec.ts
```

Expected: FAIL — the toast's right edge is past the Settings button's left
edge. Then put the `calc()` back and confirm with `git diff` that
`toast.css` is clean before moving on.

- [ ] **Step 3: Run it on the real tree to verify it passes**

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/toast-clears-activity-bar.spec.ts
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
npx prettier --write e2e/toast-clears-activity-bar.spec.ts
git add e2e/toast-clears-activity-bar.spec.ts
git commit -F - <<'EOF'
test(e2e): a toast does not cover the activity bar's global actions (nocx-c5cwl)

The overlap the rail's move created was found by reading CSS, not by a
failing test, which is the whole reason to add one: the unit assertion
Task 2 carries reads the stylesheet source, so it passes for a stylesheet
that is right and a layout that is wrong.

The toast is raised through the Git panel's copy affordance rather than by
calling showToast from the test, because a toast the test injected proves
the CSS rather than the product. Only the horizontal overlap is asserted —
both surfaces live at the bottom of the window, so a vertical overlap is
expected and says nothing.
EOF
```

---

## Follow-up beads to file (not tasks here)

- `nocx-708q` is titled "The left panel is a real multi-view sidebar". The title stops being literally true; file a bead to retitle the epic and its body, and do not fold it into this work.
- `frontend/src/ui/README.md` — the kit inventory names `ResizeHandle`'s variance. Add `pane` to its row if the table lists per-component variance; check before assuming it needs an edit.

## What the integrator runs, and the worker does not

`AGENTS.md` → "Git authority": the four CI jobs on the merged tree, once, before pushing to `main`.

```bash
make ci-full
```

The container's failure set is not CI's — layout-sensitive specs fail there and pass in CI. `e2e/vertical-tab-placement.spec.ts` and `e2e/toast-clears-activity-bar.spec.ts` are both geometry, so read a container red against CI before believing it.
