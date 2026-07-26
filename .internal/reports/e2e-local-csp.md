# E2E Local CSP Diagnosis — Environment Failure

**Date:** 2026-07-25
**Worker:** task_b16f50512ab0 / dispatch ctx_ae78491c7056
**Branch:** pr-11-boundary

## Verdict

The local e2e suite is **unusable on this VM**. `wails dev`'s Go binary crashes within
seconds of the first Playwright test connecting, killing the dev server (`:34115`) and
making every subsequent test fail with `net::ERR_CONNECTION_REFUSED`. The CSP experiment
(Step 2) was **not run** — no trustworthy baseline could be established.

## Blocker reproduction

**None of the three chromium blockers reproduced in a comparable way.**

| #   | Test                        | CI failure                                      | Local result                           | Match? |
| --- | --------------------------- | ----------------------------------------------- | -------------------------------------- | ------ |
| 1   | `activity.spec.ts:10`       | `.tab-add` click timeouts (element not visible) | `ERR_CONNECTION_REFUSED` (server dead) | ✗      |
| 2   | `enhanced-input.spec.ts:61` | Expect `"MS-2"` got `"MS-1~00701~"`             | `ERR_CONNECTION_REFUSED`               | ✗      |
| 3   | `tabs.spec.ts:8`            | `.tab-add` click timeouts                       | `ERR_CONNECTION_REFUSED`               | ✗      |

The only test that survived `page.goto` in either run was `activity-bell.spec.ts:12`
(first test in sort order, 3-worker run), which found `.tab` count = 0 instead of the
expected 1. This is not the same failure mode as CI (where `.tab-add` resolves but
is reported "not visible").

Every other test in both runs (14/15 first run with 3 workers, 14/15 second run with
1 worker) hit `ERR_CONNECTION_REFUSED` before even loading a page.

## Root cause: webkit2gtk EGL crash

The Wails Go binary starts and serves `:34115` correctly. The Vite dev server is alive.
But the moment Playwright connects and the webview (`webkit2gtk-4.1`) initializes,
the `WebKitWebProcess` crashes with **SIGABRT**. Terminal output:

```
Could not create default EGL display: EGL_BAD_PARAMETER. Aborting...
```

The process tree:

```
wails dev -tags webkit2_41
  └─ nocx-dev-linux-amd64  (listens on :34115, :33319/ws)
      └─ WebKitWebProcess  ← SIGABRT (coredump on disk, 2.3M)
```

After the web process abort, the Go binary exits, `:34115` drops, and all subsequent
Playwright `page.goto()` calls get connection refused.

### Crash evidence

```
coredumpctl list (most relevant entries):
  PID 34626  webkit2gtk-4.1/WebKitWebProcess  SIGABRT  (first run)
  PID 37029  webkit2gtk-4.1/WebKitWebProcess  SIGABRT  (single-worker run, coredump preserved)

journalctl stacktrace (from first run):
  #3 __poll (libc.so.6)
  #4 WTF::RunGLibMainLoopIteration (libjavascriptcoregtk-4.1.so.0)
  #5 WTF::RunLoop::run
  → Full trace in journal from PID 34651, 763.6M memory peak
```

Playwright webkit also crashes for the same reason (3 SIGABRT coredumps from
`minibrowser-wpe/bin/WPEWebProcess` in the same log — the known GPU issue).

## Environment details

- **OS:** NixOS, linux 6.18.39, x64, no GPU
- **Display:** Xvfb :99 (1920x1080x24) — starts fine, xkbcomp warnings only
- **Tooling:** wails v2.13.0, webkit2gtk-4.1, Playwright 1.61.1 (chromium)
- **Browsers path:** `/nix/store/...playwright-browsers` (via `/etc/set-environment`)
- **CI env:** `CI=1` is set system-wide; unset per-invocation with `CI=` prefix
- **wails dev tag:** `-tags webkit2_41` required (webkit2gtk-4.0 absent)

## Attempted runs

### Run 1 — 3 workers (baseline attempt)

```
CI= npx playwright test --project=chromium --reporter=list
```

15 tests, 1 passed (`seed.spec.ts`), 14 failed.

- Tests 1-3 (`activity-bell`, `activity`, `click-focus`) connected, found no `.tab`
  elements, timed out.
- Tests 4-15 all `ERR_CONNECTION_REFUSED`.
- Server died during the run. PID 34605 (nocx-dev-linux-amd64) exited.
- Duration: 9.1s.

### Run 2 — 1 worker (stability check)

```
CI= npx playwright test --project=chromium --reporter=list --workers=1
```

15 tests, 1 passed (`seed.spec.ts`), 14 failed.

- Test 1 (`activity-bell`): connected, found `.tab` = 0.
- Tests 2-15: all `ERR_CONNECTION_REFUSED`.
- Server died after the first test. PID 37010 exited.
- Duration: 12.7s.

## Files modified / inspected

- `frontend/index.html` — **confirmed unchanged** via `git diff HEAD -- frontend/index.html` (no output).
  CSP `<meta>` tag is present as committed. If removed for re-testing, it must be restored with
  `git checkout -- frontend/index.html`.
- `playwright.config.ts`, `package.json`, `package-lock.json`, `e2e/command-editor.spec.ts` —
  **untouched**, per brief constraints.

## What could not be verified

1. **Whether the CSP `<meta>` tag causes the 5 CI blockers.** The CSP experiment is
   blocked on environment viability: without a stable baseline, CSP removal results are
   meaningless.
2. **The `connect-src` / `default-src` / `worker-src` hypothesis.** Both `default-src 'self'`
   and the missing `worker-src` remain valid suspects for CI, but cannot be tested here.
3. **Browser console CSP violation reports.** Could not be collected because the Go
   backend never survived long enough to run a meaningful test.

## Recommendation for the coordinator

The local e2e suite needs a VM with functional GPU/EGL. Options:

1. **Run on a VM with a GPU** — even a software-renderer GPU that works with EGL
   (`LIBGL_ALWAYS_SOFTWARE=1` did not help here). The Wails webview requires EGL.
2. **Skip local reproduction; fix and verify directly on CI.** The CI failures are
   well-characterized (5 tests, all CSP-compatible symptoms). A fix could be tested by
   submitting a PR and observing the CI run.
3. **Disable the webview** in `wails dev` for headless environments. This is a framework
   change, not trivial, but would unblock all future GPU-less CI/dev-machine runs.

## Process cleanup completed

- `Xvfb :99` — killed
- `wails dev -tags webkit2_41` — killed
- `nocx-dev-linux-amd64` (2 instances) — killed
- Vite dev servers on :5173, :5174 — killed
- `xvfb-run` wrapper — killed
- Ports `:34115`, `:33319`, `:5173`, `:5174` — all free
