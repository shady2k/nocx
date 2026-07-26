# W2b — `e2e/command-editor.spec.ts`: tests that outlived their feature

You are a worker in an Orca wave. The coordinator owns the branch, the commits and the
issue tracker. Work in `/home/dev/orca/workspaces/nocx/pr-11-boundary` (branch
`pr-11-boundary`).

## Why this task exists

The Playwright suite currently has 13 tests that fail on **every** run, on both `main` and
this branch. They are long-standing rot, not a regression, and they went unnoticed because
the pre-commit hook runs vitest — Playwright only ever runs in CI, and only on non-draft
PRs to `main`.

Four of those 13 live in `e2e/command-editor.spec.ts`, two per browser project:

| Test                                                                                   | Failure                                                                 |
| -------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `command-editor.spec.ts:61` — "the submit button is clickable and submits"             | `locator.click` times out on `.nocx-editor-submit`                      |
| `command-editor.spec.ts:73` — "a multi-line command is one gutter landmark, not three" | `expect.poll(...).toBe(1)` receives `0` — no `.nocx-gutter-glyph` found |

Bead `nocx-m7x` already records the first one: `.nocx-editor-submit` was deleted from
`frontend/src/editor.ts` in commit `7204aff` ("remove submit btn"). Nothing renders that
class any more, so the test cannot pass. The bead's guidance is to delete the test — the
button is gone by design — and to check the rest of the file for other assertions that
outlived their feature.

The second one is **not** covered by that bead and you must not assume it has the same
answer. It is the interesting one.

## The distinction that matters

For each failing assertion, there are two possible worlds, and they call for opposite fixes:

1. **The feature was deliberately removed.** The test is stale. Delete it.
2. **The feature still exists and is broken.** The test is right and the product is wrong.
   Deleting it would erase the only evidence of a real bug.

Deleting a test is destroying a safety net. Do it only where you can show the feature is
gone by design — a commit that removed it, and no code that renders it today. Where you
cannot show that, **leave the test alone and report it** as a probable product bug. Coming
back with "2 deleted, 2 left alone because the feature is still there" is a better outcome
than four deletions, and much better than a silently green suite that tests nothing.

Apply that especially to `.nocx-gutter-glyph`. Find out whether gutter glyphs are still a
feature of the editor. If the code that renders them exists, the assertion is describing a
real defect and must survive.

## What to do

1. Read `e2e/command-editor.spec.ts` in full — all five tests, not just the two failing
   ones. Three currently pass (`editor is visible at the first prompt`, `mouse hit-tests
the textarea, not the terminal canvas`, `double-click selects a word in the editor`).
   Do not break them.

2. For every selector the file asserts on (`.nocx-editor-submit`, `.nocx-gutter-glyph`, and
   any others), establish whether anything in `frontend/` renders it today. Search the
   source; `git log -S '<selector>' -- frontend/` will show you when it appeared and
   disappeared. Record the commit that removed it, if it was removed.

3. Apply the fix that the evidence supports, per test:
   - feature gone by design → delete the test (and any now-unused helper or import it
     leaves behind, so the file does not accumulate dead scaffolding);
   - feature still present → leave the test untouched and write up what you found.

4. Verify what you can **without running the suite** — see the constraint below. Reading
   the file after editing to confirm it is well-formed TypeScript and that no helper is
   left dangling is the bar here.

## Hard constraint: you cannot run the e2e suite

Another worker is running Playwright against a `wails dev` server on port `:34115` right
now. Only one such server can exist. **Do not start `wails dev`, do not start `Xvfb`, and
do not run `npx playwright test`** — you would fight over the port and corrupt the other
worker's measurements.

This is fine for your task: every question you need to answer is "does this selector exist
in the source", which is answered by reading code and git history, not by running tests.
The coordinator will run the suite at the phase gate.

If you become convinced the task genuinely cannot be settled without a run, escalate and
say why — do not start a server.

## Ground rules

- No commits, no pushes, no branches. No `git stash`.
- **The only file you may modify is `e2e/command-editor.spec.ts`.** Another worker owns
  `frontend/index.html`. Do not touch `frontend/src/**`, `playwright.config.ts`,
  `package.json` or `package-lock.json`. If your conclusion is that a _product_ file needs
  changing, report that — do not change it.
- Do not run repo-wide gates (`npm test`, `tsc --noEmit`, `prettier`, `golangci-lint`).
  They compile the whole project and would observe the other worker's half-written files,
  producing phantom blockers. The coordinator runs them at the phase gate.
- Do not touch beads / `bd`. The coordinator owns the issue tracker, including `nocx-m7x`.
- Report numbers, not adjectives: tests deleted, tests kept, and for each one the evidence
  (commit sha, or the file and line that still renders the selector).
- State plainly anything you could **not** verify.

## When done

Write your findings to `.internal/reports/command-editor-specs.md`, then send `worker_done`
from your own terminal using the `taskId` and `dispatchId` from the dispatch preamble. In
the body: how many tests you deleted, how many you kept and why, and whether you found a
product bug the coordinator needs to file.
