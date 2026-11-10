# ADR-x7cvh8e1 — A tab's panel belongs to the tab, and the UI-state document keeps only the width

- **Status:** Accepted (2026-11-09, with `nocx-ouru5.1.18`; supersedes the
  `sidebar.collapsed` and `sidebar.activeViewId` half of
  [ADR-0048](0048-ui-state-is-a-document-not-a-setting.md) — the panel's view
  and its collapse are the tab's, on its layout row — and leaves that record's
  store, its settings/UI-state line and its §4 repair rule untouched)
- **Date:** 2026-11-09
- **Related:** ADR-0048 (the document this narrows; the panel is still UI
  state, and still not a setting), ADR-0098 (the window half of the same
  document), the tabs-panes-and-blocks design §4.5 (what a tab stores), and
  `e2e/panel-per-tab.spec.ts` (the criterion, end to end).

## Context

ADR-0048 settled that the app's unasked-for state is one document, and named
the sidebar's collapse and active view among it:

```json
"sidebar": { "collapsed": false, "activeViewId": "ports", "width": 206 }
```

That was written when the panel was one window-wide thing. It is not: the
panel's views speak for the tab in front — Files follows its origin, Ports
its connection — so a person who opens Ports on one tab and switches to
another is looking at a panel that belongs to a different tab, and either
keeps a panel that has nothing to say about where they are, or loses the one
they opened. The owner's criterion names the expected behaviour plainly: open
Ports on tab 2, switch to tab 1 — which has no panel open — and the rail is
collapsed; back to tab 2 and Ports is still there; and after a restart the
panel comes back with the tab.

A window-wide pair cannot express that, and the document is the wrong place
for it: one tab's choice would overwrite another's on every click.

## Decision

**The panel a tab shows is a fact about the TAB, and it lives on the tab's
layout row**, beside the colour, the name and the pinning that §4.5 already
stores there:

- `tabs.panel_view` — the sidebar view this tab shows, `""` for a tab that has
  never opened one. The store keeps a string and judges none of it; the
  renderer owns the registry of views, exactly as it does for a tab's colour,
  so a value a newer renderer understands is carried rather than refused.
- `tabs.panel_collapsed` — whether that tab's rail is collapsed. Kept beside
  the view rather than folded into it, because a collapsed panel remembers the
  view it reopens on; that is the behaviour the activity bar's toggle has
  always had, and losing it would make `Ctrl+B` twice land on a different
  view than the one the person was reading.

The pair is written by one method, `tabs.setPanel`, and travels on every
`tabs.*` result and on `layout.read` — no new store, no new document, no new
mechanism. The renderer applies the front tab's panel on every activation and
writes a person's change back to that tab's row; a choice whose write is still
in flight is what the tab shows until the row agrees, because a write is a
round trip and a person can switch away and back inside it.

**`uistate` keeps only `sidebar.width`.** Width is a fact about the window's
edge, not about any tab, and it stays where ADR-0048 put it. `collapsed` and
`activeViewId` leave the document entirely: keeping them there would be a
second owner of the same fact, and the two would disagree the first time a
person switched tabs.

Two consequences are decided here because the code has to answer them:

- **A tab with no panel shows the rail collapsed.** That is what "tab 1 (no
  panel)" means. The one exception is the tab the window opens with: it is
  seeded once per page load with the app's default panel (the first registered
  view) and the choice is recorded, which is the cold start the file-manager
  design §7 documents and `e2e/files.spec.ts` asserts. Every tab opened
  afterwards starts with no panel.
- **A pane in no chain has no row.** Settings still collapses the rail
  (nocx-3e3b's rule, unchanged); any other view pane — a diff, a file viewer —
  keeps the panel where it is, because it was opened _from_ the panel and
  collapsing it would take away the list the person is working through.

## Why this rather than the obvious alternatives

- **A per-tab map inside `uistate`.** It would keep one document, but it would
  put a fact about a tab in the window's document — the same duplication §4.5
  refuses for the label and the activity indicator, one level up. It would also
  need its own garbage collection: a map keyed by tab ids that the document
  does not otherwise know about grows forever.
- **A new store.** ADR-0048 exists to end ad-hoc stores; a second one for the
  panel would be the pattern it forbids, and a reviewer would have to ask why
  the tab's other UI facts did not move with it.
- **Storing the panel's contents too.** No. Only WHICH view is remembered —
  a Files tree's expansion, a port filter, a note's scroll are the view's own
  and are not facts about the tab.

## What the next person inherits

- The document's `sidebar` is width and nothing else; a field added there for
  anything that varies by tab is this mistake returning.
- `tabs.setPanel` is the only writer of the pair; the renderer's `PaneManager`
  owns "which tab is in front" and therefore which panel is applied.
- A tab's row is what a restart restores, so the panel comes back with the
  tab — no separate restore path, and nothing to keep in step with one.
