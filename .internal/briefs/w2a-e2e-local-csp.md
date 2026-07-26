# W2a — reproduce the branch-only e2e failures locally, then test the CSP hypothesis

You are a worker in an Orca wave. The coordinator owns the branch, the commits and the
issue tracker. Work in `/home/dev/orca/workspaces/nocx/pr-11-boundary` (branch
`pr-11-boundary`).

## Why this task exists

`pr-11-boundary` is blocked from merging. Three CI runs of the Playwright suite give this
ground truth (verified by the coordinator by diffing the failure lists, not by trusting a
summary):

- **13 tests fail on every run, on both refs** — pre-existing rot, not this branch's problem.
- **5 tests fail reproducibly on `pr-11-boundary` and pass on `main`** — these block the merge.
- **2 tests swap places between branch runs** — genuinely flaky, ignore them.

The 5 blockers:

| #   | Project  | Test                                                                                               |
| --- | -------- | -------------------------------------------------------------------------------------------------- |
| 1   | chromium | `e2e/activity.spec.ts:10` — a background tab lights the activity indicator on normal-buffer output |
| 2   | chromium | `e2e/enhanced-input.spec.ts:61` — multiple submits in succession all route raw                     |
| 3   | chromium | `e2e/tabs.spec.ts:8` — adding a second tab preserves layout with both tabs visible                 |
| 4   | webkit   | `e2e/activity-bell.spec.ts:12` — a bell lights the indicator from inside the alternate buffer      |
| 5   | webkit   | `e2e/tabs.spec.ts:8` — adding a second tab preserves layout with both tabs visible                 |

**You are chasing the three chromium ones only.** See "webkit" below.

Symptoms from the CI logs, for orientation:

- #1, #3 (and #4, #5) are all the same shape: `locator.click` times out after 60s on
  `.tab-add`. Playwright resolves the element — `<button class="tab-add">+</button>` — but
  reports **"element is not visible"**. No assertion is ever reached.
- #2 is the informative one: `expect(locator).toHaveText("MS-2")` received
  **`"MS-1~00701~"`**. The test writes OSC 0 title sequences (`printf "\033]0;MS-1\007"`).
  The title got set to `MS-1`, but the `\007` (BEL) terminator was rendered as literal text
  instead of being consumed, and the second sequence never took effect.

## Prime suspect

This branch added a Content-Security-Policy `<meta>` to `frontend/index.html`
(`script-src 'self'` with **no** `'unsafe-inline'`). It is the strongest hypothesis for
all five, and no amount of reading settles it — it needs a run. That is the core of this task.

Other candidates on the branch, for completeness: `frontend/src/log.ts` (new — now the only
owner of the Wails `Log` FFI, guards `window.go` before calling), 14 migrated call sites in
`frontend/src/tabs.ts`, `frontend/src/connections.ts` rebuilt with `createElement`/
`textContent`, and `Debug: options.Debug{}` in `main.go` (inspector off).

## Environment — read this carefully, it was set up today

The suite has never been run on this Linux VM before. The pieces are in place now:

- `xvfb-run` is installed; `wails`, GTK3 and webkit2gtk-4.1 are present.
- Playwright browsers come from the **nix store**, not from a download:
  `PLAYWRIGHT_BROWSERS_PATH` is already exported system-wide.
- `@playwright/test` is pinned to exactly `1.61.1` and is already installed at the repo
  root (`node_modules/.bin/playwright`). The nix-provided driver is the same version and
  the same browser revisions.

Rules that follow from that:

- **Never run `npx playwright install`.** The nix store is read-only; it can only fail.
- If your shell has no `PLAYWRIGHT_BROWSERS_PATH`, run `source /etc/set-environment`
  first. A shell opened before today will not have it.
- `wails dev` on this box needs the build tag: **`wails dev -tags webkit2_41`**
  (webkit2gtk-4.0 is absent, 4.1 is present). Without the tag it will not build.
- `wails dev` opens a real GTK window and **panics with `failed to init GTK` if there is no
  display** — this is confirmed, not a guess. It needs an X display even though nothing
  ever looks at the window.

`playwright.config.ts` has `command: 'wails dev'` hardcoded with no build tag, so letting
Playwright start the server itself will not work here. **Do not edit `playwright.config.ts`
— it is shared with CI.** Use the escape hatch the config already provides:
`reuseExistingServer: !process.env.CI`. Start the dev server yourself and Playwright will
attach to it:

```bash
source /etc/set-environment
Xvfb :99 -screen 0 1920x1080x24 &          # or use xvfb-run
cd /home/dev/orca/workspaces/nocx/pr-11-boundary
DISPLAY=:99 wails dev -tags webkit2_41 &   # must reach :34115 and STAY ALIVE
# wait until http://localhost:34115 answers, then:
npx playwright test --project=chromium
```

Confirm the app process is actually alive before you trust a test result — if it panicked,
`:34115` may still answer while every Go binding is dead, and you would be measuring
nothing. Say in your report how you verified this.

### webkit

**Do not chase webkit.** It is broken on this VM for reasons unrelated to nocx: the
Playwright WebKit build uses the WPE backend and aborts with
`Could not create WPE EGL display: EGL_SUCCESS. Aborting...` on this GPU-less VM, after
which any `newPage()` hangs until timeout. `WEBKIT_DISABLE_DMABUF_RENDERER=1`,
`WEBKIT_DISABLE_COMPOSITING_MODE=1`, `LIBGL_ALWAYS_SOFTWARE=1` and running under
`xvfb-run` were all tried and none helped. Always pass `--project=chromium`. If you find
yourself debugging WebKit, stop and escalate instead — you are off-task.

## What to do

### Step 1 — baseline

Get the suite running and run `--project=chromium` on the branch as it stands. Record the
full pass/fail list.

Then answer the question that decides whether local runs are usable at all: **do blockers
#1, #2 and #3 reproduce here?** Chromium is Playwright's own browser talking HTTP to
`:34115`, so the host OS should not matter — but that is an argument, not a measurement.
Report what actually happened. If they do not reproduce, say so plainly and stop before
Step 2; a local run that disagrees with CI cannot settle anything, and the coordinator
needs to know that immediately rather than after a wasted experiment.

Expect some of the 13 pre-existing failures to fail here too. That is fine and expected —
they are not your problem. Do not fix them, do not report them as new.

### Step 2 — the CSP experiment

Only if Step 1 reproduced the blockers. Remove **only** the CSP `<meta>` block from
`frontend/index.html` (leave every other line alone), re-run the same command, and diff the
results against Step 1.

Then state which of these you observed:

- All three chromium blockers pass → **CSP is the cause.**
- Some pass → **mixed**; say exactly which, because the rest have another cause.
- None change → **CSP is exonerated**; the cause is elsewhere and you have narrowed it.

If CSP is implicated, go one step further and find out _which directive_ does it, by
re-adding directives one at a time. `default-src 'self'` is worth suspecting specifically:
there is no `worker-src` in the policy, so workers fall back to `default-src` and any
`blob:`-created worker is blocked. Also check the browser console for CSP violation
reports during the run — those name the blocked resource directly and are far better
evidence than a pass/fail diff. Capture them.

**Restore `frontend/index.html` to its committed state before you finish**
(`git checkout -- frontend/index.html`) and confirm in your report that you did. The fix,
if any, is the coordinator's to commit — your job is the diagnosis.

### Step 3 — write it up

Write to `.internal/reports/e2e-local-csp.md`:

- the exact working command sequence, so the next person can reproduce in one paste;
- Step 1 results: full chromium pass/fail list, and explicitly which of #1/#2/#3 reproduced;
- Step 2 results: the diff, the verdict, any CSP violation messages captured verbatim;
- if CSP is implicated: which directive, and a proposed minimal policy that fixes the tests
  **without** reintroducing `'unsafe-inline'` in `script-src` — that exclusion is the whole
  point of the policy and is non-negotiable. If you believe it cannot be kept, say so and
  escalate rather than quietly relaxing it.

## Ground rules

- No commits, no pushes, no branches. No `git stash`.
- The only repo file you may modify is `frontend/index.html`, and only temporarily —
  restore it before finishing.
- **Do not touch** `e2e/command-editor.spec.ts`. Another worker owns that file right now.
  Do not touch `playwright.config.ts`, `package.json` or `package-lock.json`.
- Do not run repo-wide gates (`npm test`, `tsc --noEmit`, `golangci-lint`, `prettier`,
  `gofumpt`). The coordinator runs those at the phase gate.
- Do not touch beads / `bd`. The coordinator owns the issue tracker.
- Kill your `Xvfb` and `wails dev` processes when you finish, so the next wave gets a clean
  `:34115`.
- Report numbers, not adjectives. Counts before and after, exact test ids.
- If something blocks you, escalate rather than guessing. In particular, escalate if you
  cannot get `wails dev` to stay alive — that is environment work, not test work.
- State plainly anything you could **not** verify. Silence there will be read as "everything
  above is evidence-backed", so do not leave it empty unless that is true.

## When done

Send `worker_done` from your own terminal, using the `taskId` and `dispatchId` from the
dispatch preamble. In the body: whether the blockers reproduced, the CSP verdict, the
directive if you found it, and the report path.
