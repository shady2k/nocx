# E2E Failure Inventory — `pr-11-boundary` vs `main`

Generated: 2026-07-25 from CI logs (`main` run 30162745654, `pr-11-boundary` run 30162351263)

> **Coordinator correction — read this before the tables below.**
>
> The buckets in this report are wrong in both directions. It was written from two runs;
> a third run (`30163136292`, also on `pr-11-boundary`) plus a direct diff of the three
> failure lists gives this ground truth:
>
> | Bucket                                          | This report | Verified |
> | ----------------------------------------------- | ----------- | -------- |
> | Fail on every run, both refs (pre-existing)     | 11          | **13**   |
> | Fail reproducibly on the branch only (blockers) | 2           | **5**    |
> | Genuinely unstable                              | 7           | **2**    |
>
> Two specific errors:
>
> 1. `[chromium] enhanced-input.spec.ts:38 Ctrl-C` is listed as PRE-EXISTING (FAIL/FAIL).
>    It actually **fails on `main` and passes on the branch**. That single misclassification
>    is why this report accounts for 20 branch failures when CI reported 19 — the
>    arithmetic was the tell.
> 2. Four failures called FLAKY-SUSPECT (`activity` chromium, `tabs` both projects,
>    `activity-bell` webkit) reproduce on **both** branch runs and on neither `main` run.
>    They are blockers, not flakes. Bare timeouts are not evidence of flakiness; the third
>    run is.
>
> The five verified blockers, and the two genuinely unstable tests, are listed in
> `.internal/briefs/w2a-e2e-local-csp.md`. Everything below is retained for its per-test
> evidence and error text, which is accurate and useful — only the bucketing is not.

## Summary

| Bucket                                                                              | Count |
| ----------------------------------------------------------------------------------- | ----- |
| **PRE-EXISTING** (fail on both refs, same assertion)                                | 11    |
| **BRANCH-REGRESSION** (pass on main, fail on branch, assertion-backed)              | 2     |
| **FLAKY-SUSPECT** (bare timeout, differing errors between runs, or cannot classify) | 7     |
| **SKIPPED** (WebKit clipboard — explicit skip)                                      | 2     |
| Always-passing                                                                      | 10    |
| **Total**                                                                           | 32    |

### Per-file breakdown

| Spec file                | Pre-existing                                           | Branch-regression                 | Flaky-suspect            | Always-pass                                                                  | Skipped    |
| ------------------------ | ------------------------------------------------------ | --------------------------------- | ------------------------ | ---------------------------------------------------------------------------- | ---------- |
| `activity.spec.ts`       | —                                                      | —                                 | chromium, webkit         | —                                                                            | —          |
| `activity-bell.spec.ts`  | chromium                                               | —                                 | webkit                   | —                                                                            | —          |
| `click-focus.spec.ts`    | —                                                      | —                                 | chromium, webkit         | —                                                                            | —          |
| `clipboard.spec.ts`      | chromium (copy-on-select), chromium (paste)            | —                                 | —                        | —                                                                            | webkit (2) |
| `command-editor.spec.ts` | chromium (submit, gutter), webkit (submit, gutter)     | —                                 | —                        | chromium (visible, hit-test, dblclick), webkit (visible, hit-test, dblclick) | —          |
| `enhanced-input.spec.ts` | chromium (Ctrl-C), webkit (read, Ctrl-C, multi-submit) | **chromium** (read, multi-submit) | —                        | —                                                                            | —          |
| `tab-title.spec.ts`      | —                                                      | —                                 | —                        | chromium, webkit                                                             | —          |
| `tabs.spec.ts`           | —                                                      | —                                 | **chromium**, **webkit** | —                                                                            | —          |
| `seed.spec.ts`           | —                                                      | —                                 | —                        | chromium, webkit                                                             | —          |

---

## Full Test Inventory

### `e2e/activity.spec.ts`

| #   | Project  | Test                                                                   | main | pr       | Bucket            | Evidence                                                                                                                                                                                                                                                                 |
| --- | -------- | ---------------------------------------------------------------------- | ---- | -------- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | chromium | a background tab lights the activity indicator on normal-buffer output | PASS | **FAIL** | **FLAKY-SUSPECT** | `locator.click: Test timeout 60000ms` on `.tab-add`. Resolved to `<button class="tab-add">+</button>` but "element is not visible". Bare timeout — no assertion reached. Cannot tell from logs alone whether this is a regression or a flake.                            |
| 2   | webkit   | a background tab lights the activity indicator on normal-buffer output | FAIL | FAIL     | **FLAKY-SUSPECT** | **Different errors:** main = `expect(locator).toBeAttached()` — expected `tab-activity` indicator, not found. pr = `locator.click` timeout on `.tab-add` — "element is not visible". Same ref but different failure paths — cannot classify as same bug from logs alone. |

### `e2e/activity-bell.spec.ts`

| #   | Project  | Test                                                         | main | pr       | Bucket            | Evidence                                                                                                            |
| --- | -------- | ------------------------------------------------------------ | ---- | -------- | ----------------- | ------------------------------------------------------------------------------------------------------------------- |
| 3   | chromium | a bell lights the indicator from inside the alternate buffer | FAIL | FAIL     | PRE-EXISTING      | `locator.click: Test timeout 60000ms` on `.tab-add`. "element is not visible". Same on both refs.                   |
| 4   | webkit   | a bell lights the indicator from inside the alternate buffer | PASS | **FAIL** | **FLAKY-SUSPECT** | `locator.click: Test timeout 60000ms` on `.tab-add`. "element is not visible". Bare timeout — no assertion reached. |

### `e2e/click-focus.spec.ts`

| #   | Project  | Test                                                        | main | pr   | Bucket            | Evidence                                                                                                                                                                                                                       |
| --- | -------- | ----------------------------------------------------------- | ---- | ---- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 5   | chromium | a click into the pane leaves the terminal taking keystrokes | FAIL | FAIL | **FLAKY-SUSPECT** | **Different errors:** main = `expect.toContain("nocx-editor-input")` Received `""`. pr = `locator.click` timeout on `.tabbar-spacer` "element is not visible". Failure text differs run to run — cannot prove same root cause. |
| 6   | webkit   | a click into the pane leaves the terminal taking keystrokes | FAIL | FAIL | **FLAKY-SUSPECT** | Same pattern: main = `expect.toContain` assertion; pr = tabbar-spacer click timeout. Different failure text between runs.                                                                                                      |

### `e2e/clipboard.spec.ts`

| #   | Project  | Test                                                               | main | pr   | Bucket       | Evidence                                                                                     |
| --- | -------- | ------------------------------------------------------------------ | ---- | ---- | ------------ | -------------------------------------------------------------------------------------------- |
| 7   | chromium | copy-on-select: selecting terminal text copies it to the clipboard | FAIL | FAIL | PRE-EXISTING | `expect.toHaveText` — Expected `"CT-ms0i..."`, Received `"Users/runner"`. Same on both refs. |
| 8   | chromium | paste: right-click pastes clipboard text at the cursor             | FAIL | FAIL | PRE-EXISTING | `expect.toHaveText` — Expected `"PT-ms0i..."`, Received `"Users/runner"`. Same on both refs. |
| 9   | webkit   | copy-on-select                                                     | —    | —    | SKIPPED      | `test.skip(browserName !== "chromium")`                                                      |
| 10  | webkit   | paste                                                              | —    | —    | SKIPPED      | `test.skip(browserName !== "chromium")`                                                      |

### `e2e/command-editor.spec.ts`

| #   | Project  | Test                                                   | main | pr   | Bucket       | Evidence                                                                                                                                                           |
| --- | -------- | ------------------------------------------------------ | ---- | ---- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 11  | chromium | editor is visible at the first prompt                  | PASS | PASS | —            | Passing on both refs.                                                                                                                                              |
| 12  | chromium | mouse hit-tests the textarea, not the terminal canvas  | PASS | PASS | —            | Passing on both refs.                                                                                                                                              |
| 13  | chromium | double-click selects a word in the editor              | PASS | PASS | —            | Passing on both refs.                                                                                                                                              |
| 14  | chromium | the submit button is clickable and submits             | FAIL | FAIL | PRE-EXISTING | `locator.click: Test timeout 60000ms` on `.nocx-editor-submit`. **Known dead selector — `.nocx-editor-submit` deleted in commit `7204aff`**. No longer in the DOM. |
| 15  | chromium | a multi-line command is one gutter landmark, not three | FAIL | FAIL | PRE-EXISTING | `expect.poll.toBe(1)` — Received `0`. No `.nocx-gutter-glyph` elements found after submit. Same on both refs.                                                      |
| 16  | webkit   | editor is visible at the first prompt                  | PASS | PASS | —            | Passing on both refs.                                                                                                                                              |
| 17  | webkit   | mouse hit-tests the textarea, not the terminal canvas  | PASS | PASS | —            | Passing on both refs.                                                                                                                                              |
| 18  | webkit   | double-click selects a word in the editor              | PASS | PASS | —            | Passing on both refs.                                                                                                                                              |
| 19  | webkit   | the submit button is clickable and submits             | FAIL | FAIL | PRE-EXISTING | Same dead `.nocx-editor-submit` selector.                                                                                                                          |
| 20  | webkit   | a multi-line command is one gutter landmark, not three | FAIL | FAIL | PRE-EXISTING | Same gutter glyph count assertion.                                                                                                                                 |

### `e2e/enhanced-input.spec.ts`

| #   | Project  | Test                                              | main | pr       | Bucket                | Evidence                                                                                                                                                                                                                                   |
| --- | -------- | ------------------------------------------------- | ---- | -------- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 21  | chromium | read command receives input after enhanced submit | PASS | **FAIL** | **BRANCH-REGRESSION** | `expect.toHaveText("got-hello")` — Received `"Users/runner"` after 5000ms timeout. Title never updated — either the command never executed or the title-set path is broken. Assertion-backed.                                              |
| 22  | chromium | Ctrl-C at a prompt does not trap input            | FAIL | FAIL     | PRE-EXISTING          | `expect.toHaveText(marker)` — Received `"Users/runner"`. Same on both refs.                                                                                                                                                                |
| 23  | chromium | multiple submits in succession all route raw      | PASS | **FAIL** | **BRANCH-REGRESSION** | `expect.toHaveText("MS-2")` — Received `"MS-1~00701~"`. The `~00701~` is the escape character `\007` leaked as literal text — the second OSC 0 sequence was partially parsed but the `\007` terminator was not stripped. Assertion-backed. |
| 24  | webkit   | read command receives input after enhanced submit | FAIL | FAIL     | PRE-EXISTING          | `expect.toHaveText("got-hello")` — Received `"got-"`. `read` builtin ran but received empty string — keystrokes didn't reach the PTY. Same on both refs.                                                                                   |
| 25  | webkit   | Ctrl-C at a prompt does not trap input            | FAIL | FAIL     | PRE-EXISTING          | `expect.toHaveText(marker)` — Received `"Users/runner"`. Same on both refs.                                                                                                                                                                |
| 26  | webkit   | multiple submits in succession all route raw      | FAIL | FAIL     | PRE-EXISTING          | `expect.toHaveText("MS-2")` — Received `"Users/runner"`. Same on both refs.                                                                                                                                                                |

### `e2e/tab-title.spec.ts`

| #   | Project  | Test                                             | main | pr   | Bucket | Evidence              |
| --- | -------- | ------------------------------------------------ | ---- | ---- | ------ | --------------------- |
| 27  | chromium | a new tab never displays "Terminal" in its title | PASS | PASS | —      | Passing on both refs. |
| 28  | webkit   | a new tab never displays "Terminal" in its title | PASS | PASS | —      | Passing on both refs. |

### `e2e/tabs.spec.ts`

| #   | Project  | Test                                                        | main | pr       | Bucket            | Evidence                                                                                                                         |
| --- | -------- | ----------------------------------------------------------- | ---- | -------- | ----------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| 29  | chromium | adding a second tab preserves layout with both tabs visible | PASS | **FAIL** | **FLAKY-SUSPECT** | `locator.click: Test timeout 60000ms` on `.tab-add`. Resolved but "element is not visible". Bare timeout — no assertion reached. |
| 30  | webkit   | adding a second tab preserves layout with both tabs visible | PASS | **FAIL** | **FLAKY-SUSPECT** | Same `.tab-add` invisible pattern. Bare timeout.                                                                                 |

### `e2e/seed.spec.ts`

| #   | Project  | Test | main | pr   | Bucket | Evidence              |
| --- | -------- | ---- | ---- | ---- | ------ | --------------------- |
| 31  | chromium | seed | PASS | PASS | —      | Stub — no assertions. |
| 32  | webkit   | seed | PASS | PASS | —      | Stub — no assertions. |

---

## Branch-Regression Analysis (2 tests)

Both confirmed regressions are assertion-backed (not bare timeouts) and both are in `enhanced-input.spec.ts` on chromium:

**1. `[chromium] enhanced-input.spec.ts:10 — read command receives input after enhanced submit`**

- main: PASS. pr: `expect.toHaveText("got-hello")` — Received `"Users/runner"`.
- The shell is alive (title shows "Users/runner" = default CWD), but the typed commands never executed or the title-update OSC 0 sequence never reached xterm. The title stayed at the shell's default persistent CWD announcement, which means no command completed.

**2. `[chromium] enhanced-input.spec.ts:61 — multiple submits in succession all route raw`**

- main: PASS. pr: `expect.toHaveText("MS-2")` — Received `"MS-1~00701~"`.
- This is the most informative error on the branch. `MS-1~00701~` means the second OSC 0 escape sequence (`printf "\033]0;MS-1\007"`) was partially executed: xterm processed the title change to "MS-1" but the `\007` (bell/string terminator) was rendered literally as `~00701~` instead of being consumed as a control character. The `~00701~` is a consequence of the ANSI escape parser failing to handle the escape sequence correctly, or the escape arrived in a form that the frontend/backend treated as literal text.

### Cause hypothesis for the 2 regressions

Both regressions point to the same class of problem: keystrokes not reaching the PTY correctly, or ANSI output not being rendered correctly. Two branch changes are relevant:

1. **`frontend/src/log.ts` (new file)** — This is the single owner of the Wails `Log` FFI. It guards `window.go` before calling. While `log.ts` shouldn't affect keystroke routing, a bug in it could swallow errors that would otherwise signal a deeper problem.

2. **`frontend/src/tabs.ts`** — One log line that serialised `sshOpts` was **deleted** (it leaked an SSH password). This is a purely cosmetic change and unlikely to cause regressions.

3. **`frontend/src/connections.ts`** — The credential panel was rebuilt with `createElement`/`textContent` instead of `innerHTML`. Unlikely to affect general keystroke routing.

4. **`frontend/index.html` CSP** — The `script-src 'self'` (no `'unsafe-inline'`) CSP is the strongest candidate. If the Wails runtime bootstrap is injected as an inline script by `wails dev`, CSP blocks it, and the frontend loses its Go bindings. `Log`, `WindowSetTitle`, and the PTY data path all go through Wails bindings. A blocked runtime explains both "title never updates" and "escape sequences displayed literally" — xterm is running but the Go-side processing or the binding bridge is broken.

5. **`main.go` inspector off** — `Debug: options.Debug{}` disables the Wails DevTools inspector. This does **not** affect the Wails runtime injection or Playwright's browser CDP connection. Not implicated.

**However**, the CSP hypothesis has a gap: `script-src` controls script execution, not CSS layout or element visibility. The `.tab-add` being in the DOM but invisible (which drives the 4 FLAKY-SUSPECT failures) is a CSS/rendering concern — not directly attributable to script-src blocking. If the invisible-element failures are genuine regressions rather than flakes, they may have a different root cause than the enhanced-input assertion failures. The logs alone cannot determine this.

### Dead selector note

The `.nocx-editor-submit` element was deleted from `frontend/src/editor.ts` in commit `7204aff`. This causes:

- `[chromium] command-editor.spec.ts:61` — `locator.click` timeout (pre-existing)
- `[webkit] command-editor.spec.ts:61` — same (pre-existing)

These are definitively **not** branch regressions — the selector has been gone since `7204aff` which was committed before either CI run. The same root cause applies to the `.nocx-gutter-glyph` assertions in test `command-editor.spec.ts:73` — the glyph implementation may have been removed alongside the submit button.

---

## CSP Verdict

**Not ruled out, but not confirmed from logs alone.** The reasoning:

- **What supports it:** The 2 BRANCH-REGRESSION assertion failures involve broken command execution and ANSI escape handling. If the Wails runtime (injected during `wails dev`) provides the bridge between frontend keystrokes and backend PTY, and if that injection uses inline scripts that CSP blocks, the symptoms match.

- **What weakens it:** (a) `script-src` cannot explain the `.tab-add` element being in the DOM but invisible — that's a CSS/layout issue that would need a different CSP directive (`style-src`) or a different root cause entirely. (b) The harness (`harness.ts`) does **not** use `addInitScript` in CI — the `NOCX_WS_PORT` gate is unset, so the harness is a no-op. (c) CI logs contain no CSP violation warnings (which Chromium emits as console errors when CSP blocks a resource). The logs include console output from tests, and no CSP violation appears.

- **What would confirm it:** Remove the CSP `<meta>` from `frontend/index.html` and re-run CI. If the 2 regressions and the 4 flaky-suspect failures all vanish, CSP is the cause. If only some vanish, the cause is mixed.

- **About the Wails inspector (main.go):** `Debug: options.Debug{}` disables `OpenInspectorOnStartup`. This has no effect on runtime behavior, CSP enforcement, or Playwright's browser CDP connection. Not implicated.

---

## What I could not determine

1. **Why the 11 pre-existing failures exist.** They fail identically on both refs with the same assertion errors. Root causes predate this PR.

2. **Whether the 7 FLAKY-SUSPECT failures are genuine regressions or CI flakes.** Four are bare timeouts (`.tab-add` invisible on activity chromium, activity-bell webkit, tabs both) — suspiciously consistent but no assertion reached. Three differ between runs (activity webkit, click-focus both) — the error text changed from assertion failure to click timeout, meaning the failure moved earlier in execution. This could be a regression that changed the failure surface, or independent flakiness. Without a third run or the same assertion failing on both, cannot classify.

3. **Whether the 4 bare-timeout failures share a cause with the 3 changed-error failures.** The `.tab-add` invisibility on activity chromium, activity-bell webkit, and both tabs tests is the same symptom: button in DOM but not visible. The click-focus tests hit a different invisible element (`.tabbar-spacer`). Activity webkit hit `.tab-add` invisibility on PR but `toBeAttached` assertion failure on main. All could be the same underlying UI initialization issue or independent — logs alone cannot separate them.

4. **How Wails v2 injects its runtime during `wails dev`.** Whether it uses inline `<script>` tags (blocked by CSP), external `<script src>` tags (allowed), or `page.addInitScript` (allowed). This determines whether CSP is or is not the root cause, but it's not observable from CI job logs.
