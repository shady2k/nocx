# Worker brief — delete the wterm renderer (bead `nocx-k7kq`)

## Why

The renderer bake-off is settled: xterm.js won (`docs/decisions/0001-xterm-js-as-vt-frontend.md`).
Keeping a second implementation costs more than it buys, and it actively **degrades the
interface** — `TerminalRenderer` currently advertises behaviour wterm silently does not have,
which is filed as `nocx-au6`.

Keep the **interface**. Delete the **second implementation**. ADR-0001's durable value is that
the VT frontend is replaceable behind `TerminalRenderer`; that value survives having exactly one
implementation today. What does not survive is a union type, a `?r=` switch and a set of comments
that shape the contract around a renderer nobody uses.

## Scope, already measured — verify it rather than trusting it

- **`frontend/src/renderers/wterm.ts`** — delete.
- **`frontend/src/renderers/index.ts`** — `RendererName` becomes a single value, so the union,
  the `case 'wterm'` branch, the `?r=xterm|wterm` parsing and the "diagnostics escape hatch"
  comment all go. If `createRenderer` / the name-picking become vestigial once there is one
  implementation, **collapse them** — `AGENTS.md` forbids dead code, and an unused indirection
  invites misuse.
- **`frontend/src/renderers/types.ts`** — two comments concede the contract to wterm
  (`@wterm/dom never fires`, `has no equivalent event — the callback is never …`). Removing those
  concessions is what **actually** retires `nocx-au6`: the interface should stop describing a
  renderer that no longer exists, and any member that was optional _only_ because wterm could not
  honour it should become non-optional. Read each one and decide deliberately — do not blanket
  delete comments and leave the shape unchanged.
- **`frontend/package.json`** — drop the `@wterm/dom` dependency. Do not run a broad
  `npm install` that rewrites unrelated lockfile entries; keep the lockfile change to this removal.
- **Type ripple** into `frontend/src/tabs.ts` and `frontend/src/terminal-content.ts` wherever
  `RendererName` is threaded through constructors.

## Docs — seven files, and one of them is a decision record

`docs/vision.md`, `docs/architecture.md`, `README.md`, `AGENTS.md`,
`docs/superpowers/plans/2026-07-23-warp-command-experience-m1-foundation.md`,
`docs/decisions/0005-linux-webkitgtk-forced-refresh-pump.md`, and
`docs/decisions/0001-xterm-js-as-vt-frontend.md`.

**ADR-0001 is the sensitive one.** It records that wterm stays switchable behind
`TerminalRenderer` for re-testing. Removing wterm _changes that decision_, so amend the ADR
**deliberately** — add a superseding/amendment note stating that the second implementation was
removed, when, and why — rather than quietly editing the sentence so it reads as if the decision
was always this. `AGENTS.md` is explicit: a decision that turns out wrong gets changed on
purpose, never routed around.

ADR-0005 (Linux/WebKitGTK forced-refresh pump) mentions wterm as context. Check whether the
_mechanism_ it documents is xterm-specific or renderer-agnostic before touching it — if the pump
exists for reasons unrelated to wterm, leave the mechanism alone and only correct the reference.

For the historical plan document under `docs/superpowers/plans/`, prefer leaving history intact:
it records what was planned at the time. Correct it only if it would mislead someone about the
current state; say which choice you made and why.

## A wart that disappears with the file — do not port it forward

`wterm.ts`'s `fitViewport` approximated cell width as `FONT_SIZE` — a full em advance — where a
monospace advance is nearer 0.6em, so its computed column count was materially wrong. It goes away
with the file. Do not carry the approximation into any shared helper.

## Files you own

`frontend/src/renderers/**`, `frontend/src/tabs.ts`, `frontend/src/terminal-content.ts`,
`frontend/package.json` + lockfile, and the seven documentation files listed above, plus tests for
what you change.

**Another worker is active in this same worktree on a different task.** It owns
`frontend/src/main.ts`, `frontend/src/settings.ts`, `frontend/src/settings.test.ts`,
`frontend/src/settings-content.ts` and `frontend/src/style.css`. Do **not** touch any of those.
If removing wterm turns out to require a change in `main.ts`, **escalate** — the coordinator will
make that edit — do not make it yourself.

## Bootstrap

```bash
cd frontend && npm ci && cd ..
```

## Verification

Because a second worker is editing this worktree, **do not run repo-wide gates** — you would
observe its half-written files and report a phantom blocker. Scope verification to your own files:

```bash
cd frontend
npx tsc --noEmit 2>&1 | grep -E 'renderers/|tabs\.ts|terminal-content\.ts'   # your files only
npx eslint src/renderers src/tabs.ts src/terminal-content.ts
npx prettier --check src/renderers src/tabs.ts src/terminal-content.ts
npx vitest run src/renderers src/tabs.test.ts
```

Note honestly in your report that a whole-project typecheck was **not** run and why — the
coordinator runs the full gate at the phase gate, after both workers finish.

The Playwright e2e suite is **red on `main`** (13 failures, `nocx-bw2`) and is not in the
per-commit gate. Do not run it, do not chase it, do not claim anything about it.

## Ground rules

- **Do not commit, push or branch.** The coordinator owns git.
- **Do not touch the issue tracker.** No `bd` commands — `nocx-au6` and `nocx-id3` are retired by
  this work, but the coordinator closes them.
- **If you finish early, STOP and report.** Do not start adjacent work.
- **Report the real output of the checks you ran, and say plainly which checks you did not run.**
  Three worker reports on this programme have already claimed a clean gate that was not clean; the
  next one will be verified line by line, so a false claim only costs you a rejected round.
- Report the file list from actual `git status --porcelain` output, pasted, not from memory.
- Format only the files you changed.
- Report numbers, not adjectives: how many references removed per file, which interface members
  became non-optional, and which docs you deliberately left alone.
- **State explicitly anything you could not verify.**
