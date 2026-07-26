# W4 — restore the horizontal tab bar on `pr-11-boundary` (bead nocx-x4z)

You are a worker in an Orca wave. The coordinator owns the branch, the commits and the
issue tracker. Work in `/home/dev/orca/workspaces/nocx/pr-11-boundary` (branch
`pr-11-boundary`).

## Why this task exists

Commit `557e87d` ("feat(ssh): SSH Connection Manager epic", 52 files, +8025/−605) did more
than its message says. Alongside the SSH work it replaced the app shell:

- deleted `<div id="tabbar"></div>` from `frontend/index.html`;
- kept the `new TabManager(bar, ...)` call but handed it a `display: none` div, so the
  horizontal tab strip is still rendered — invisibly;
- hand-rolled a second, visible tab list in `main.ts` as a view inside the sidebar panel;
- moved the update notice out of the tab bar and into the sidebar.

Consequence: `.tab-add` exists in the DOM but sits inside a hidden element, so Playwright
resolves it and waits for visibility until it times out. That is why exactly four e2e tests
fail on this branch and pass on `main`:

| Project  | Test                           |
| -------- | ------------------------------ |
| chromium | `e2e/activity.spec.ts:10`      |
| chromium | `e2e/tabs.spec.ts:8`           |
| webkit   | `e2e/tabs.spec.ts:8`           |
| webkit   | `e2e/activity-bell.spec.ts:12` |

The owner has decided the redesign is worth doing, but as a **setting** — horizontal stays
the default, vertical Warp-style becomes an option — and it belongs in its own epic
(`nocx-d3q`), not in a branch about the credential boundary. So restore the tab bar here.

**This is not throwaway work.** Horizontal is the default mode of the future epic; you are
restoring the default, not undoing a feature.

## What to restore

Compare against `main` and put the shell back:

```bash
git diff origin/main...HEAD -- frontend/index.html frontend/src/main.ts
```

- `frontend/index.html`: bring back `<div id="tabbar"></div>` as the first child of `#app`,
  above `#body`. **Leave the CSP `<meta>` exactly as it is** — it is this branch's security
  work and is unrelated (it was investigated and cleared as a cause of these failures).
- `frontend/src/main.ts`: resolve `#tabbar`, pass it to `TabManager`, and restore the
  missing-element guard so a broken shell fails loudly instead of rendering into nothing.
- Delete the `hiddenBar` container entirely. Nothing may exist solely to satisfy a
  constructor.
- Delete the hand-rolled sidebar session list — the `mount` body of the `sessions` view
  that builds the "Open Sessions" header, the `.sidebar-add-btn`, the
  `.sidebar-session-item` rows and the `tm.onTabsChanged = renderSessions` subscription.
  With a real tab bar back, that is a duplicate implementation of the same UI.
- Restore the update notice to the tab bar. `main` has an `UpdateNotice` class that renders
  into the bar; `557e87d` replaced it with a `.update-notice-sidebar` element prepended to
  the sidebar panel plus loose `showUpdateAvailable` / etc. functions. Take whichever shape
  is cleaner, but the notice must end up in the tab bar and the sidebar must not keep a
  dead copy.

## What to keep — do not remove these

Everything that is genuinely the SSH Connection Manager:

- the **Connections** view in the activity bar, its icon, `action: 'tab'` and
  `onActivate: () => tm.newManagerTab()`;
- `ProfileClient` and its wiring (`client.rawSocket()`);
- the extra `TabManager` constructor parameter (`profileClient`) and everything in
  `tabs.ts` that supports manager tabs;
- `frontend/src/connections.ts`, `profiles.ts`, `ipc.ts`, `log.ts` and their styles;
- the CSP `<meta>` in `index.html`;
- tab drag-and-drop, if it works independently of placement.

The **Sessions** view is the judgement call. With the tab bar back it has no content left —
its whole body was the duplicate tab list. Removing the view entirely is the honest outcome;
`main` had it only as a placeholder. If you decide to keep it as a placeholder to match
`main`, say so and why. Do not leave it mounting an empty panel by accident.

## Verification

You **can** run the suite locally — the environment was fixed today. Read
`.internal/reports/e2e-local-csp-round2.md` for the working recipe before improvising; it
has the exact command sequence and the traps.

Key points from it:

- `source /etc/set-environment` first, or `PLAYWRIGHT_BROWSERS_PATH` will be missing.
- **Never run `npx playwright install`** — browsers come from the read-only nix store.
- `wails dev` needs `-tags webkit2_41` and a display; use `Xvfb`/`xvfb-run`.
- `CI=1` is set system-wide. Prefix your test command with `CI=` or Playwright ignores your
  server and tries to start its own.
- `libEGL warning: DRI3 error` under Xvfb is benign.
- `playwright.config.ts` is shared with CI — **do not edit it**. Start the dev server
  yourself; `reuseExistingServer` attaches to it.

Run the suite and report the four blockers specifically. Note that the local run is **not**
identical to CI: three `command-editor` tests fail locally but pass in CI, and several
pre-existing failures differ. Do not chase those — 13 failures are known to predate this
branch (`nocx-bw2`) and are not yours. Judge yourself only on the four.

Do not modify any file under `e2e/`. If the four blockers do not go green without touching a
test, that is a finding — report it, do not edit the test to fit.

## Ground rules

- No commits, no pushes, no branches. No `git stash`.
- Files you may modify: `frontend/index.html`, `frontend/src/main.ts`, and
  `frontend/src/style.css` only if a rule became dead with the sidebar list. Nothing else
  without escalating.
- Do not touch `e2e/**`, `playwright.config.ts`, `package.json`, `package-lock.json`,
  `frontend/vite.config.ts`, or any Go file.
- Do not touch beads / `bd`. The coordinator owns the issue tracker.
- Kill `Xvfb` and `wails dev` when you finish; confirm `:34115` and `:5173` are free.
- Report numbers, not adjectives: the four blockers before and after, and the full local
  pass/fail counts.
- State plainly anything you could **not** verify.

## When done

Write `.internal/reports/tabbar-restore.md` with the diff summary, the before/after results
for the four blockers, and your decision on the Sessions view with its reasoning. Then send
`worker_done` from your own terminal using the `taskId` and `dispatchId` from the dispatch
preamble.
