# W3 — reproduce the 5 branch-only e2e failures locally, then test the CSP hypothesis

You are a worker in an Orca wave. The coordinator owns the branch, the commits and the
issue tracker. Work in `/home/dev/orca/workspaces/nocx/pr-11-boundary` (branch
`pr-11-boundary`).

## Why this task exists

`pr-11-boundary` cannot merge until five e2e tests are explained. Three CI runs were
diffed against each other to separate real breakage from noise:

- **13 tests fail on every run, on both refs** — pre-existing rot, tracked as `nocx-bw2`.
  Not your problem. Expect to see them fail locally too; do not fix them, do not report
  them as new.
- **5 tests fail reproducibly on `pr-11-boundary` and pass on `main`** — these block the
  merge. They are your target.
- **2 tests swap places between branch runs** — genuinely flaky, ignore them
  (`enhanced-input.spec.ts:10` and `:38` on chromium).

The 5 blockers:

| #   | Project  | Test                                                                                               |
| --- | -------- | -------------------------------------------------------------------------------------------------- |
| 1   | chromium | `e2e/activity.spec.ts:10` — a background tab lights the activity indicator on normal-buffer output |
| 2   | chromium | `e2e/enhanced-input.spec.ts:61` — multiple submits in succession all route raw                     |
| 3   | chromium | `e2e/tabs.spec.ts:8` — adding a second tab preserves layout with both tabs visible                 |
| 4   | webkit   | `e2e/activity-bell.spec.ts:12` — a bell lights the indicator from inside the alternate buffer      |
| 5   | webkit   | `e2e/tabs.spec.ts:8` — adding a second tab preserves layout with both tabs visible                 |

Symptoms in CI, for orientation:

- #1, #3, #4, #5 are one shape: `locator.click` times out after 60s on `.tab-add`.
  Playwright resolves the element — `<button class="tab-add">+</button>` — but reports
  **"element is not visible"**. No assertion is ever reached.
- #2 is the informative one: `expect(locator).toHaveText("MS-2")` received
  **`"MS-1~00701~"`**. The test writes OSC 0 title sequences (`printf "\033]0;MS-1\007"`).
  The title reached `MS-1`, but the `\007` (BEL) terminator was rendered as literal text
  instead of being consumed, and the second sequence never took effect.

## Prime suspect

This branch added a Content-Security-Policy `<meta>` to `frontend/index.html`
(`script-src 'self'`, with **no** `'unsafe-inline'`). It is the strongest hypothesis for
all five and cannot be settled by reading — it needs a run. That is the core of this task.

Other branch changes, for completeness: `frontend/src/log.ts` (new — sole owner of the
Wails `Log` FFI, guards `window.go` before calling), 14 migrated call sites in
`frontend/src/tabs.ts`, `frontend/src/connections.ts` rebuilt with `createElement` /
`textContent`, and `Debug: options.Debug{}` in `main.go` (inspector off).

## Environment

**A previous worker attempted this task and failed on the environment.** Read
`.internal/reports/e2e-local-csp.md` before you start — it documents exactly how far the
setup got and what broke. Do not re-derive it.

What blocked it: the VM had no EGL implementation at all (`/run/opengl-driver` did not
exist), so the app's `WebKitWebProcess` aborted with `EGL_BAD_PARAMETER` seconds into the
first test, taking `:34115` down and leaving every later test with
`ERR_CONNECTION_REFUSED`.

**That is fixed now.** `hardware.graphics.enable = true` was deployed; Mesa is present and
`eglinfo` reports `llvmpipe (LLVM 21.1.8, 256 bits)`. All three Playwright engines were
verified end to end (launch, goto, textContent, screenshot) — **webkit included**. So both
webkit blockers are in scope for you; ignore the earlier report's advice to skip webkit and
its Docker recommendation, both of which are now obsolete.

Setup facts that still hold:

- `wails dev` opens a real GTK window and **panics with `failed to init GTK` without a
  display**. Use Xvfb.
- `wails dev` needs the build tag on this box: **`wails dev -tags webkit2_41`**
  (webkit2gtk-4.0 is absent, 4.1 present). Without it, it will not build.
- Playwright browsers come from the **nix store**; `PLAYWRIGHT_BROWSERS_PATH` is exported
  system-wide. **Never run `npx playwright install`** — the store is read-only and it can
  only fail. If your shell lacks the variable, `source /etc/set-environment`.
- `@playwright/test` is pinned to exactly `1.61.1` and installed at the repo root.
- **`CI=1` is set system-wide on this VM.** `playwright.config.ts` keys
  `reuseExistingServer` off `!process.env.CI`, so you must prefix your test command with
  `CI=` or Playwright will ignore your server and try to start its own.
- Under Xvfb, `eglinfo` and the app print `libEGL warning: DRI3 error: Could not get DRI3
device`. **This is benign** — the X11 path probes for hardware DRI3 and falls back to
  software. Do not chase it.

`playwright.config.ts` hardcodes `command: 'wails dev'` with no build tag, so letting
Playwright start the server will not work here. **Do not edit `playwright.config.ts` — it
is shared with CI.** Use the escape hatch it already provides:

```bash
source /etc/set-environment
Xvfb :99 -screen 0 1920x1080x24 &
cd /home/dev/orca/workspaces/nocx/pr-11-boundary
DISPLAY=:99 wails dev -tags webkit2_41 &     # must reach :34115 and STAY ALIVE
# wait until http://localhost:34115 answers, then:
CI= npx playwright test
```

Before trusting any result, confirm the app process is still alive — the previous run's
whole failure mode was a dead backend serving nothing. Say in your report how you checked.

## What to do

### Step 1 — baseline

Run the full suite (both projects) on the branch as it stands. Record the complete
pass/fail list, then answer the question that decides everything downstream: **do blockers
#1–#5 reproduce here?**

Report what happened, per blocker. If they do not reproduce, say so plainly and **stop
before Step 2** — a local run that disagrees with CI cannot settle anything, and the
coordinator needs to know immediately rather than after a wasted experiment.

### Step 2 — the CSP experiment

Only if Step 1 reproduced the blockers. Remove **only** the CSP `<meta>` block from
`frontend/index.html`, leaving every other line alone. Re-run the same command. Diff
against Step 1.

State which you observed:

- All five blockers pass → **CSP is the cause.**
- Some pass → **mixed**; say exactly which, because the rest have another cause.
- None change → **CSP is exonerated**, and you have narrowed the search.

### Step 3 — if CSP is implicated, find the directive

Re-add directives one at a time until the failures return. `default-src 'self'` deserves
specific suspicion: the policy declares no `worker-src`, so workers fall back to
`default-src` and any `blob:`-created worker is blocked.

Better evidence than a pass/fail diff: **capture the browser console**. Chromium emits a
CSP violation report naming the blocked resource directly. Wire up
`page.on('console')` / `page.on('pageerror')` in a scratch script, or use the Playwright
trace already configured (`trace: 'retain-on-failure'`). Quote any violation verbatim.

**Restore `frontend/index.html` to its committed state before you finish**
(`git checkout -- frontend/index.html`) and say in your report that you did. The fix, if
any, is the coordinator's to commit — your job is the diagnosis.

### Step 4 — write it up

Write to `.internal/reports/e2e-local-csp-round2.md` (a **new** file; leave the old report
in place as the record of the environment failure):

- the exact working command sequence, so the next person reproduces in one paste;
- Step 1: full pass/fail list for both projects, and explicitly which of #1–#5 reproduced;
- Step 2: the diff and the verdict;
- Step 3: the directive, and any CSP violation text captured verbatim;
- if CSP is implicated, a proposed minimal policy that fixes the tests **without**
  reintroducing `'unsafe-inline'` in `script-src`. That exclusion is the entire point of
  the policy and is non-negotiable — if you believe it cannot be kept, escalate and say
  why rather than quietly relaxing it.

## Ground rules

- No commits, no pushes, no branches. No `git stash`.
- The only repo file you may modify is `frontend/index.html`, and only temporarily.
  Do not touch `playwright.config.ts`, `package.json`, `package-lock.json`,
  `e2e/**`, `frontend/src/**` or `frontend/vite.config.ts`.
- Do not run repo-wide gates (`npm test`, `tsc --noEmit`, `golangci-lint`, `prettier`,
  `gofumpt`). The coordinator runs those at the phase gate.
- Do not touch beads / `bd`. The coordinator owns the issue tracker.
- Kill `Xvfb` and `wails dev` when you finish, and confirm `:34115`, `:5173` are free.
- Report numbers, not adjectives: counts and exact test ids, before and after.
- Escalate rather than guess if you are blocked — especially if `wails dev` will not stay
  alive. That is environment work, and the coordinator wants to know immediately.
- State plainly anything you could **not** verify. An empty "could not verify" section is a
  claim that everything else is evidence-backed; do not write one unless that is true.

## When done

Send `worker_done` from your own terminal using the `taskId` and `dispatchId` from the
dispatch preamble. In the body: which blockers reproduced, the CSP verdict, the directive
if you found it, and the report path.
