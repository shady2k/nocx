# The activity bar and its panel move to the window's right edge

**Date:** 2026-08-29
**Bead:** nocx-c5cwl (brainstorming session)
**Status:** approved by the owner; awaiting an implementation plan

## The problem

With `tab.placement === 'vertical'` the shell reads, left to right:

```
activity bar | sidebar panel | tab strip | panes
```

The tab strip selects the tab. Three of the six panel views are scoped **to
the selected tab** — they render for whatever is in front and change when the
front tab changes. So the panel is downstream of the tab selector and sits
upstream of it. That is the dissonance the owner reported.

Horizontal placement does not have it: `#tabbar` is a sibling of `#workspace`
spanning the full width above everything (`frontend/src/App.tsx:29-31`), so
containment reads correctly — the strip dominates, the bar is beneath it.

### The scopes, measured

The registry is `frontend/src/main.tsx:1115-1122`.

| View          | Scope      | Evidence                                                                                |
| ------------- | ---------- | --------------------------------------------------------------------------------------- |
| Files         | active tab | consumes `props.activeOrigin` — `frontend/src/files/files-view.tsx:913`                 |
| Ports         | active tab | consumes `props.activeProfileId` — `frontend/src/main.tsx:1013`                         |
| Git           | active tab | consumes `props.activeOrigin` — `frontend/src/git/git-view.tsx:138`                     |
| Notes         | window     | takes no shell props — `frontend/src/notes/notes-view.tsx:115`                          |
| Operations    | window     | reads the window-wide transfer model — `frontend/src/operations/operations-view.tsx:92` |
| Notifications | window     | reads the global feed — `frontend/src/main.tsx:1095`                                    |

The two entries in the bar's bottom zone (API workbench, Settings) are
**actions**, not views: they open a tab and never touch the panel
(`frontend/src/main.tsx:1135`).

**Three of six, not six of six — and that is what makes the argument, not what
weakens it.** A window-scoped view is equally at home on either edge. A
tab-scoped view placed _before_ the tab selector is wrong. So the right edge is
correct-or-neutral for all six; the left edge is wrong for three. The asymmetry
is the reason, and it is the one to write into the code.

`frontend/src/sidebar.tsx:345` currently asserts "Every sidebar view speaks for
the machine a terminal tab is on", which the table above falsifies. It is fixed
as part of this work.

## The decision

**The activity bar and its panel sit at the window's right edge, in both tab
placements, unconditionally.**

Rejected, with reasons:

- **Orientation-conditional layout** (bar left when horizontal, between strip
  and panes when vertical). Semantically the cleanest of the three, and it is
  the option an outside review still preferred on semantics alone. Rejected by
  the owner because chrome that changes sides when a display preference changes
  costs more than the ordering it buys: the user re-learns where the rail is
  every time they flip the setting.
- **A left/right setting.** There is one right answer here; a setting defers
  the decision, doubles the layout surface and doubles the tests that pin it.

## What changes

### 1. Shell order

`frontend/src/App.tsx` — `#body` becomes:

```
#vertical-tabstrip | #panes | #sidebar | #activitybar
```

The comment currently justifying the order ("the activity bar is app-level
chrome and belongs to the window edge") is replaced by the asymmetry above.

### 2. `ResizeHandle` grows a `pane` variant

`frontend/src/ui/resize-handle.tsx` measures the pane **before** the separator:
`clamp(startValue + (position - startPos))` (`endDrag`), and ArrowRight is
`live + step` (`:178`). The sidebar's handle moves to the panel's **leading**
edge, so the measured pane is now _after_ the separator.

Add `pane?: 'before' | 'after'`, default `'before'` — today's behaviour, so
every existing caller is unchanged. A variant rather than a second component,
for the reason already documented in that file for `orientation`: everything
else about a resize edge — the capture, the clamping, the commit-once rule, the
idle-gesture suppression — is identical.

**What inverts is the mapping from a gesture to a width, and only for the
gestures that are physical.** The file already names the rule this generalises
— "the growing key is the one that points AWAY from the pane being measured"
(`:174-176`); `pane` is what lets that sentence be true on both sides.

Physical means "moves the separator": the pointer, and the two arrow keys on
the handle's own axis (Left/Right for `vertical`, Up/Down for `horizontal`).
Those invert. The two **off-axis** arrows do not move anything — on a vertical
separator today ArrowUp is `+step` and ArrowDown is `-step` (`:187-190`), which
is APG's "Up and Right increase" convention, not a direction on screen. They
keep meaning increase and decrease, on both sides. `Home` and `End` are
absolute (`:192-196`) and are untouched.

For `pane='after'` on a vertical separator — the sidebar's case:

| Gesture       | Separator moves | Width               |
| ------------- | --------------- | ------------------- |
| pointer left  | left            | grows               |
| pointer right | right           | shrinks             |
| ArrowLeft     | left            | grows               |
| ArrowRight    | right           | shrinks             |
| ArrowUp       | —               | grows (unchanged)   |
| ArrowDown     | —               | shrinks (unchanged) |
| Home          | —               | `min` (unchanged)   |
| End           | —               | `max` (unchanged)   |

`aria-valuenow` keeps reporting the measured pane's width, so it decreases on
ArrowRight. This is the WAI-ARIA window-splitter pattern: the arrow moves the
splitter physically, and the value describes the controlled pane.

### 3. Paint and DOM order

- `#activitybar` and `#sidebar` (`frontend/src/style.css:122-149`):
  `border-right` → `border-left`.
- `PanelRoot` (`frontend/src/sidebar.tsx:222-244`): the `ResizeHandle` becomes
  the **first** child, so DOM order matches visual order. Its comment ("the
  flex row's trailing slot") is updated; the invariant it protects — a real
  flex item, never an overlay, so it can neither cover the view's scrollbar nor
  be covered by it — is unchanged.
- `.tabstrip-vertical` (`frontend/src/styles/components/tab-strip.css:111-138`)
  is **untouched**: the strip stays leftmost, so its `border-right` and its
  handle at `right: 0` remain correct.

### 4. The toast area steps clear of the rail

`.ui-toast-host` is `position: fixed; right: var(--space-4); bottom:
var(--space-4)` (`frontend/src/styles/components/toast.css:9-12`) and
`.ui-toast` takes `pointer-events: auto` back. Today that hovers over terminal
content. After the move it lands exactly on the rail's bottom zone — the API
workbench and Settings buttons (`frontend/src/sidebar.tsx:541`) — and a
`danger` toast is sticky (`frontend/src/ui/toast.tsx:60`), so it would sit on
top of two global actions until dismissed.

Fix: the rail's width becomes a token, `--activity-bar-width: 48px`, replacing
the literal in `style.css:125`, and the toast host reads it:

```css
right: calc(var(--space-4) + var(--activity-bar-width));
```

One owner for the number. The **panel** is deliberately not cleared: it
collapses and it resizes, and a toast over it is no worse than a toast over a
pane today. The rail is the surface that is always present and always holds
global actions.

### 5. The ordering trade, recorded rather than hidden

With the rail rightmost, keyboard and assistive traversal reach the panel's
content **before** the toolbar that selects which panel is shown
(`frontend/src/sidebar.tsx:6` states that contract). The move therefore repairs
tab-selector-before-tab-scoped-content and introduces
selected-panel-before-panel-selector.

This is accepted, not mitigated. Reordering the DOM while painting the reverse
with `order` would make DOM order disagree with visual order, which is a worse
accessibility defect than the one it fixes. VS Code's "Move Primary Side Bar
Right" has the identical property and ships.

## Tests

Rule 1 (`AGENTS.md`): each of these asserts something a user can do or see, not
what the code currently does.

**New**

- `frontend/src/ui/resize-handle.test.tsx` — `pane='after'`: pointer left
  grows; ArrowRight shrinks and `aria-valuenow` decreases; ArrowLeft grows;
  ArrowUp still grows and ArrowDown still shrinks (the off-axis keys do not
  invert); Home and End still select `min` and `max`.
- `e2e/vertical-tab-placement.spec.ts` — the invariant, asserted in **both**
  placements: the activity bar's left edge is at or right of every pane's right
  edge, and `#panes` keeps non-zero width.
- One browser assertion that a visible toast does not cover the Settings
  button — the finding in §4 is only a finding because nothing watched for it.

**Changed**

- `e2e/vertical-tab-placement.spec.ts` — the first test currently asserts "the
  strip sits right of the activity bar" and is replaced by the above.
- `e2e/sidebar-resize.spec.ts:86` — `dragHandle` drags `startX + dx`; the sign
  flips. The prose and expectations at `:160`, `:177`, `:184` and `:210` are
  reconciled with it, including the keyboard case (240 → 248 on ArrowRight
  becomes 240 → 232).
- `frontend/src/ui/resize-handle.test.tsx:37` and
  `frontend/src/sidebar.test.tsx:313` pin "ArrowRight grows". They stay, now
  explicitly as the `pane='before'` default.

**Unaffected**

- `frontend/src/sidebar.test.tsx` order-of-children-_inside_-the-bar
  assertions.

## Verified as position-independent

Checked so the plan does not go looking again:

- `frontend/src/ui/menu-geometry.ts:51` — context menus clamp both axes to the
  viewport.
- `frontend/src/ui/floating-panel.ts:404` — anchored panels clamp both edges
  against the anchor's live viewport rect.
- `frontend/src/panes.ts:577` — pane delivery responds to width and height, not
  to x.
- `frontend/src/terminal-content.ts:3394` — terminal fitting measures the
  scroller's usable width.
- Tab drag-and-drop and pane file drops carry no absolute-x assumption.
- macOS window controls: Wails hides the title bar (`main.go:127`), `#tabbar`
  stays at the top as the drag region and owns the traffic-light inset
  (`frontend/src/styles/components/tab-strip.css:17`). Moving chrome away from
  the _lower_ left edge does not touch that path.

## Stale comments this invalidates

Each is rewritten in the same commit as the change it describes:

- `frontend/src/App.tsx:33-42` — the ordering rationale.
- `frontend/src/style.css:112`, `:137` — "activity bar | sidebar | panes".
- `frontend/src/sidebar.tsx:200`, `:229` — "trailing edge" / "trailing slot".
- `frontend/src/sidebar.tsx:345` — "Every sidebar view speaks for the machine a
  terminal tab is on" (three of six do not).
- `frontend/src/ui/resize-handle.tsx:39` — "what the sidebar has always been".

## Deliberately out of scope

- A left/right setting.
- Renaming the open epic `nocx-708q` ("The left panel is a real multi-view
  sidebar") — its title stops being literally true. A separate bead.
- The bar's contents (`nocx-708q.1`).
- Moving the vertical tab strip, or changing anything about horizontal
  placement other than which edge the rail is on.
