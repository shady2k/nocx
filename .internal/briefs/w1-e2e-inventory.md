# W1 — e2e failure inventory (investigation only, no code changes)

You are a worker in an Orca wave. The coordinator owns the branch, the commits and the
issue tracker. **You change no source files in this task.** Your only output is one
report file plus a `worker_done` message.

## Context

Repo: `nocx` — Go backend + xterm.js frontend + Wails v2 desktop.
Your worktree: `/home/dev/orca/workspaces/nocx/pr-11-boundary` (branch `pr-11-boundary`).

CI (`.github/workflows/ci.yml`) has three jobs: `backend`, `frontend`, `e2e`.
The `e2e` job runs Playwright on macOS against a real `wails dev` app.

Two CI runs were just triggered manually (`workflow_dispatch`) so we have a clean
comparison of the same suite on both branches:

| run id        | ref              | e2e result                      |
| ------------- | ---------------- | ------------------------------- |
| `30162745654` | `main`           | 14 failed, 16 passed, 2 skipped |
| `30162351263` | `pr-11-boundary` | 19 failed, 11 passed, 2 skipped |

So **14 failures pre-date this branch** and **the branch appears to break ~5 more**.
Those two groups need completely different treatment, and right now nobody knows which
test is in which group. That is what you are producing.

Note the suite has 32 tests total in both runs, so no tests were added or removed —
every difference is a status change.

## What to do

1. Pull both e2e job logs. Working commands (run from the worktree):

   ```bash
   JID=$(gh run view <RUN_ID> -R shady2k/nocx --json jobs \
          -q '.jobs[]|select(.name=="e2e").databaseId')
   gh api "repos/shady2k/nocx/actions/jobs/$JID/logs" > /tmp/e2e_<ref>.log
   ```

   `gh run view --log` does **not** work for these (it needs the whole run and errors
   on in-progress runs); the `gh api .../jobs/<id>/logs` form above is the one that works.

2. Build the inventory. For **every one of the 32 tests**, record:
   - spec file (`e2e/*.spec.ts`) and test title,
   - status on `main`, status on `pr-11-boundary`,
   - for each failure: the assertion or timeout line, and the actual-vs-expected values.

3. Classify each failure into exactly one bucket, and say which evidence put it there:
   - **PRE-EXISTING** — fails on both refs.
   - **BRANCH-REGRESSION** — passes on `main`, fails on `pr-11-boundary`. These are the
     important ones; the branch is blocked on them.
   - **FLAKY-SUSPECT** — you cannot tell the two apart, e.g. a bare timeout with no
     assertion, or a failure whose text differs run to run. Do not force a bucket you
     cannot defend; say "cannot classify from logs alone" and explain what evidence is
     missing.

4. For each BRANCH-REGRESSION, form a hypothesis about the cause and name the specific
   commit or file you suspect. The branch's relevant changes are:
   - `frontend/src/log.ts` — **new file**, now the single owner of the Wails `Log` FFI.
     It guards `window.go` before calling, and takes `(msg, fields?)` instead of a
     pre-stringified line. 14 call sites in `frontend/src/tabs.ts` were migrated to it.
   - `frontend/src/tabs.ts` — one log line that serialised `sshOpts` was **deleted**
     (it leaked an SSH password).
   - `frontend/src/connections.ts` — the credential panel was rebuilt with
     `createElement`/`textContent` instead of an `innerHTML` template.
   - `frontend/index.html` — a Content-Security-Policy `<meta>` was added.
     `script-src` is `'self'` with **no** `'unsafe-inline'`.
   - `main.go` — `Debug: options.Debug{}`, i.e. the Wails inspector is off.

   **Weigh the CSP hypothesis carefully and first.** Several failures look like the UI
   never reacted (`locator.click` timeouts) or a tab title never updated
   (`toHaveText` receiving `"Users/runner"` — a path fragment — instead of the expected
   marker). If the CSP blocks something the app or the Playwright harness relies on, that
   is exactly the shape it would take. Check `e2e/harness.ts` for anything that would be
   blocked — `page.addInitScript`, `page.evaluate`, an injected `<script>`, an inline
   handler. Say plainly whether the CSP is or is not implicated, and on what evidence.
   `main.go`'s disabled inspector is a second candidate for the same symptom: check
   whether the harness attaches over the devtools/CDP port.

   Also relevant: bead `nocx-m7x` records that
   `e2e/command-editor.spec.ts` ("the submit button is clickable and submits") clicks
   `.nocx-editor-submit`, an element deleted from `frontend/src/editor.ts` in commit
   `7204aff`. That one is a known dead selector — confirm it is in your PRE-EXISTING
   bucket and check the rest of that file for other assertions that outlived their feature.

5. Write the report to
   `/home/dev/orca/workspaces/nocx/pr-11-boundary/.internal/reports/e2e-inventory.md`.

   Structure it so the coordinator can split the fixes across parallel workers **by spec
   file**: a per-file section, with its failures grouped by bucket, plus a summary table
   at the top counting failures per file per bucket. File-level ownership is how the next
   wave will be split, so a failure attributed to the wrong file costs a whole worker.

6. Finish with a "what I could not determine" section. If you could not classify
   something, or a hypothesis is a guess rather than a finding, say so there explicitly.
   An empty section is a claim that everything above is evidence-backed — do not write
   one unless that is true.

## Ground rules

- **No source file changes.** No commits, no pushes, no branches, no `git stash`.
  The only file you create is your report (and scratch files under `/tmp`).
- Do not run `npx playwright test` locally. This is a Linux VM; the suite needs macOS and
  a real windowing system, and a local run would tell you nothing about the CI failures.
  Everything you need is in the two logs.
- Do not run repo-wide gates (`npm test`, `tsc --noEmit`, `golangci-lint`, `prettier`).
  Another wave owns those, and the coordinator runs them at the phase gate.
- Do not touch beads / `bd`. The coordinator owns the issue tracker.
- Report numbers, not adjectives. "14 pre-existing, 5 regressions, 1 unclassifiable"
  is useful; "mostly pre-existing" is not.

## When done

Send `worker_done` from **your own terminal**, using the `taskId` and `dispatchId` from
the dispatch preamble at the top of your session. In the body, give the three bucket
counts, the per-file distribution, your CSP verdict, and the report path.
