# Worker brief — TABS-1 remaining scope: the dead accessor surface (bead `nocx-d3q.1`)

## What is already done, so you do not redo it

This bead's title — "extract tab rendering behind an interface — TabManager stops owning its DOM
placement" — **has already been delivered** by the `TabStrip` presentation port that landed earlier.
`TabManager` no longer builds the tab-list container or reorders button DOM; chrome creation,
placement, ARIA, roving `tabindex` and drag-reorder all live behind the port, and the horizontal
implementation is in `frontend/src/tab-strip.ts`.

**Do not touch the port.** Do not re-extract anything.

## What remains, and it is the whole task

The bead's own body carries a second requirement that was never done. Quoting it:

> `557e87d` added three members to `TabManager` purely to feed the hand-rolled sidebar list —
> `getTabs()`, `findTab(id)` and the optional `onTabsChanged` hook. With the sidebar list removed,
> `getTabs` and `findTab` have zero callers and `onTabsChanged` has no subscriber. They were
> deliberately left in place rather than deleted in the restore commit: this task owns the
> rendering seam and will either adopt them as the seam's accessor surface or delete them. Do not
> leave them as-is — `AGENTS.md` forbids dead code, and an unused public API invites misuse.

I re-measured this on your base commit, so it is current and not stale:

```
getTabs          declared in tabs.ts   callers elsewhere: NONE
findTab          declared in tabs.ts   callers elsewhere: NONE
onTabsChanged    4 references in tabs.ts   subscribers elsewhere: NONE
```

`onTabsChanged` is the interesting one: it is _fired_ from inside `tabs.ts` (on close, on reorder,
on create) but nothing subscribes, so those call sites are dead weight too — deleting the hook means
deleting its invocations, not just its declaration.

## The decision is yours, and it must be argued

The bead offers two options and forbids a third (leaving them). Pick one and justify it in your
report:

1. **Delete all three**, including the `onTabsChanged?.()` invocation sites. Simplest, and correct
   if nothing plausibly needs them. But check first: the vertical tab panel is coming
   (`nocx-d3q.3`) — would a vertical strip want to observe tab-list changes? Read `tab-strip.ts`
   and see whether the port already covers that need through its own intents. If the port already
   emits what a future consumer needs, these are redundant and go.
2. **Adopt them into the port's accessor surface**, i.e. they become part of the `TabStrip`-facing
   contract with a real caller. Only defensible if you can point at the caller you are adding.
   "A future task might want it" is not a caller — that is YAGNI, which `AGENTS.md` forbids.

My reading, which you are free to overturn with evidence: option 1. The port was designed so that
placement implementations receive intents rather than polling a tab list, so an observer hook and
two list accessors look like leftovers from the design that the port replaced. But I have not read
`tab-strip.ts` closely enough to be certain, and you will — so if you find the port genuinely needs
them, say so and take option 2.

## Files you own

`frontend/src/tabs.ts`, `frontend/src/tabs.test.ts`, and `frontend/src/test-support/tabs-fixtures.ts`
if a fixture references the removed members.

**Two other workers are active in this same worktree.** One owns `frontend/src/settings-content.ts`
and `frontend/src/style.css`; the other is on a different branch entirely. Do **not** touch
`settings-content.ts`, `settings.ts`, `style.css`, `main.ts`, `tab-strip.ts`, `tab-content.ts` or
`terminal-content.ts`. If removing a member turns out to require a change in one of those,
**escalate** — the coordinator will make that edit.

## Verification — scoped, because you are sharing a worktree

**Do not run repo-wide gates.** You would observe a neighbour's half-written file and report a
phantom blocker. That has already happened once on this programme, to a worker that behaved
correctly under a wrong instruction.

```bash
cd frontend
npx tsc --noEmit 2>&1 | grep -E 'tabs\.ts|tabs\.test\.ts|tabs-fixtures'
npx eslint src/tabs.ts src/tabs.test.ts src/test-support/tabs-fixtures.ts
npx prettier --check src/tabs.ts src/tabs.test.ts src/test-support/tabs-fixtures.ts
npx vitest run src/tabs.test.ts
```

Say plainly in your report that a whole-project typecheck was **not** run and why. The coordinator
runs the full gate at the phase gate, after all workers finish.

Playwright is red on `main` and is not in the per-commit gate; a separate worker owns it right now.
Do not run it, do not chase it, do not claim anything about it.

## Ground rules

- **Do not commit, push or branch.** The coordinator owns git.
- **Do not touch the issue tracker.** No `bd` commands.
- **If you finish early, STOP and report.** This is a small task; finishing early is expected and
  starting adjacent work is not.
- Format only the files you changed. No repo-wide `prettier --write`.
- Report numbers, not adjectives: how many references removed, which invocation sites went, test
  count before and after.
- **State explicitly anything you could not verify.**
