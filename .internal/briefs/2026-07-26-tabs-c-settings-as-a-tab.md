# Worker brief — TABS-C: Settings as a dedicated tab (bead `nocx-h4e2`)

## Why this task exists

This is the task that fixes the defect the whole programme started from. Settings are currently
mounted inside the collapsible sidebar panel (`frontend/src/main.ts`, the settings view is
registered with `action: 'panel'`). That panel **cannot** host the screen, and the reason is
arithmetic rather than taste:

- `#sidebar` is **240px** wide with `overflow: hidden` (`frontend/src/style.css:701-708`)
- `.st-view` adds `16px 24px` padding, leaving **192px** of content width
- `.st-label-col` is declared `flex: 0 0 200px` (`frontend/src/style.css:1507`) — a fixed column
  **wider than the box containing it**

So the label column alone overflows, the control column is pushed past the right edge, and
`overflow: hidden` clips it. The user sees a toggle sliced in half by the panel edge and a label
wrapped into three lines.

## What already exists — build on it, do not rebuild it

Everything you need has landed on this branch and on the settings branch:

- **`TabContent` / `TabHost` / `ContentViewport` seam** with a cancellable lifecycle
  (`AbortSignal` through `mount`, idempotent `dispose`, host inert after disposal), branded
  `SurfaceType` / `SingletonKey` registered at the composition root, and `restoreDescriptor`
  carrying view-tab policy as model state. `Tab` is a single class with no subclasses.
- **`ConnectionsContent`** is the worked example: it is a singleton view tab already wired
  through `TabManager.openTab(content, descriptor)` in `main.ts`. Follow that shape.
- **`HorizontalTabStrip`** behind the placement port, with roving `tabindex`, Left/Right/Home/End
  and full ARIA.
- **The generated settings screen** (`frontend/src/settings.ts`) already has Customized/Default
  from provenance, a Reset action, a modified-only filter, a search bar, `dataClass` treatment,
  declared bound display, and a `LoadState` enum separating loading / ready / failed / empty.

Your job is the **container and the layout**, not the settings controls.

## Read first

- `/home/dev/repos/nocx/.internal/specs/2026-07-26-tab-and-settings-foundation-design.md`
  — **Part C in full**, plus B.4/B.6/B.7 for the seam contract you are implementing against.
- `AGENTS.md` — binding. TDD, SRP, interface-first, no compatibility shims, YAGNI.

## What to build

1. **`SettingsContent` implementing `TabContent`.** Its singleton key and surface type are
   registered at the composition root alongside the Connections ones — `nocx.settings`. Inactive
   Settings **stays mounted** until the tab is closed, so the search query, selected section and
   scroll position survive tab switches.

2. **Entry points.** The activity-bar gear changes from `action: 'panel'` to opening/focusing the
   singleton Settings tab. `Cmd/Ctrl+,` does the same. Opening twice must focus the existing tab,
   not create a second one — `TabManager`'s singleton path already does this; use it rather than
   adding your own check.

3. **Width-responsive two-pane layout.** Not content-count-responsive — this distinction was
   argued out and settled, so do not "improve" it back:

```
Wide viewport                              Narrow viewport
┌───────────────┬──────────────────────┐   ┌────────────────────────┐
│ Search        │ Settings             │   │ Settings               │
│               │                      │   │ Search                 │
│ Clipboard     │ Clipboard            │   │ Category picker        │
│               │ generated rows…      │   │ generated rows…        │
│ Modified (0)  │                      │   │                        │
└───────────────┴──────────────────────┘   └────────────────────────┘
```

- Rail width `clamp(240px, 28vw, 360px)`. The breakpoint is chosen by whether **both columns
  remain usable**, not by copying a reference screenshot's dimensions.
- The content column scrolls **independently** of the rail.
- The rail is **structurally permanent**. Only one section (`Clipboard`) is declared today, so
  it will render exactly one item and look sparse. **That is accepted and intended.** It is not
  dead code: it carries search, section navigation, the modified-only filter and its count, and
  it exercises the same layout, selection, focus and scroll-synchronisation code that stays in
  production. Four more declarations are already filed and unblocked (`nocx-8yg.5`, `.6`, `.7`,
  copy-on-select). Do **not** make the rail appear or disappear based on how many sections
  exist.

4. **Deep link.** A route resolving to the Settings surface plus a setting key must: open or focus
   the tab, clear a filter that would hide the target row, scroll it into view, focus its control,
   and briefly highlight it.

   **Carry-forward you must respect:** row ids come from `keyToDomId(key)` =
   `'st-setting-' + encodeURIComponent(key)`. `encodeURIComponent` does **not** escape `.`, so
   `st-setting-clipboard.osc52Suppressed` is a valid HTML5 id but splits into id + class inside a
   raw CSS selector. Use `getElementById` or `CSS.escape` — never `querySelector('#' + id)`.

5. **The panel afterwards.** Once settings leave it, the sidebar panel contains only the empty
   `sessions` placeholder. That is the same state `main` was in before, and `nocx-eab` (the SSH
   host list) will fill it. Do not delete the panel and do not invent content for it.

## Explicitly not in this task

Do not add settings **controls**, declarations, or bucket-2 declaration metadata (section
ordering, icons, nesting, search aliases, units). Do not implement "Open settings file" — it is
deferred infrastructure needing host reveal integration. Do not touch the placement epic's
vertical strip.

## Files you own

`frontend/src/settings-content.ts` (new) and its test, `frontend/src/main.ts` (the settings
wiring), `frontend/src/settings.ts` and `frontend/src/settings.test.ts` (only what the container
change requires — the controls themselves are done), `frontend/src/style.css` (the `.st-*` layout
rules), plus any new test-support fixtures.

Do **not** touch `frontend/src/tabs.ts`, `frontend/src/tab-strip.ts`,
`frontend/src/tab-content.ts`, `frontend/src/terminal-content.ts` or anything under `internal/`.
If the seam genuinely lacks something you need, **escalate with your reasoning** rather than
widening it yourself.

## Bootstrap

```bash
cd frontend && npm ci && cd ..
```

## Verification — required, on the FINAL state of the tree

```bash
cd frontend && npm run format:check && npm run lint && npm run typecheck && npm run test
cd .. && gofumpt -l . && golangci-lint run ./... && go test -race -count=1 ./...
```

Tests that matter more than rendering assertions: opening Settings twice yields one tab; `Cmd+,`
focuses an existing Settings tab from a terminal tab and from alternate-screen state; closing and
reopening Settings creates a fresh view; a save while a filter is active does not make the row
vanish unexpectedly; a deep link into a filtered section reveals and focuses it; Settings never
enters a restore record.

The Playwright e2e suite is **red on `main`** (13 failures, `nocx-bw2`) and is not in the
per-commit gate. Do not run it, do not chase it, do not claim anything about it.

## Ground rules

- **Do not commit, push or branch.** The coordinator owns git.
- **Do not touch the issue tracker.** No `bd` commands.
- **If you finish early, STOP and report.** Do not start adjacent work; say what you think is
  needed and stop.
- **Re-run the whole gate on the final state of the tree and paste the real output.** A gate claim
  that does not match the tree is the worst failure mode available to you here — it has happened
  on this programme already.
- Report the file list from actual `git status --porcelain` output, pasted, not from memory.
- No `prettier --write` or `gofumpt -w` across the repo; format only the files you changed.
- Report numbers, not adjectives.
- **State explicitly anything you could not verify** — in particular, jsdom cannot exercise real
  layout, so say plainly that the 192px-overflow fix is unverified by automated tests and what you
  did instead (e.g. asserted the computed layout contract you can reach, and left the visual
  confirmation to the coordinator).
