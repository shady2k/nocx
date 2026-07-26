# E2E Local CSP Diagnosis — Round 2

**Date:** 2026-07-25
**Worker:** task_f760bd63c58a / dispatch ctx_e84d3a0de2a4
**Branch:** pr-11-boundary

## Verdict

**CSP experiment not run — stop condition triggered.** Only 4/5 CI blockers reproduced locally. Blocker #2 (`enhanced-input.spec.ts:61`) passed on both chromium and webkit. Per the brief, a local run that disagrees with CI cannot settle the CSP question, so Step 2 was not attempted.

The environment (EGL/Mesa) is now functional — this is not an env failure. The previous worker's entire blocker (webkit SIGABRT from `EGL_BAD_PARAMETER`) is resolved.

## Working command sequence

```bash
source /etc/set-environment
cd /home/dev/orca/workspaces/nocx/pr-11-boundary

xvfb-run --auto-servernum -e /tmp/xvfb-wails.log bash -c '
  echo "DISPLAY=$DISPLAY" > /tmp/xvfb-display.txt; wails dev -tags webkit2_41 > /tmp/wails-dev.log 2>&1 &
  WAILS_PID=$!
  # Wait until :34115 responds
  for i in $(seq 1 120); do
    if curl -s -o /dev/null http://localhost:34115 2>/dev/null; then break; fi
    sleep 1
  done
  echo "Server ready"
  wait $WAILS_PID
' &

# Wait for xvfb-run to choose a display and start the server, then read it:
sleep 5
DISPLAY=$(cat /tmp/xvfb-display.txt | sed 's/DISPLAY=//')
echo "Using DISPLAY=$DISPLAY"


# Run tests (CI= to override system-wide CI=1, so playwright reuses existing server):
CI= DISPLAY=$DISPLAY npx playwright test --reporter=list

# Verify server alive after run:
curl -s -o /dev/null -w "%{http_code}" http://localhost:34115
pgrep -af "nocx-dev-linux" | head -1

# Cleanup:
pgrep -f "xvfb-run" | xargs -r kill
pgrep -f "wails dev" | xargs -r kill
pgrep -f "Xvfb" | xargs -r kill
```

## Step 1 — Full pass/fail list (30 tests, both projects)

Server was confirmed alive before and after the run (HTTP 200, `nocx-dev-linux-amd64` PID present). Stats: 10 passed, 2 skipped, 18 failed. 0 flaky.

### Chromium (15 tests)

| Status              | Test                                                                                           |
| ------------------- | ---------------------------------------------------------------------------------------------- |
| ✘ FAIL (1m timeout) | `activity-bell.spec.ts:12` — a bell lights the indicator from inside the alternate buffer      |
| ✘ FAIL (1m timeout) | `activity.spec.ts:10` — a background tab lights the activity indicator on normal-buffer output |
| ✘ FAIL (1m timeout) | `click-focus.spec.ts:26` — a click into the pane leaves the terminal taking keystrokes         |
| ✘ FAIL (3.5s)       | `clipboard.spec.ts:46` — selecting terminal text copies it to the clipboard                    |
| ✘ FAIL (3.4s)       | `clipboard.spec.ts:115` — right-click pastes clipboard text at the cursor                      |
| ✘ FAIL (8.5s)       | `command-editor.spec.ts:18` — editor is visible at the first prompt                            |
| ✘ FAIL (8.3s)       | `command-editor.spec.ts:26` — mouse hit-tests the textarea, not the terminal canvas            |
| ✘ FAIL (8.4s)       | `command-editor.spec.ts:44` — double-click selects a word in the editor                        |
| ✘ FAIL (8.3s)       | `command-editor.spec.ts:62` — a multi-line command is one gutter landmark, not three           |
| ✓ PASS (359ms)      | `enhanced-input.spec.ts:61` — multiple submits in succession all route raw                     |
| ✓ PASS (466ms)      | `enhanced-input.spec.ts:10` — read command receives input after enhanced submit                |
| ✓ PASS (409ms)      | `enhanced-input.spec.ts:38` — Ctrl-C at a prompt does not trap input                           |
| ✓ PASS (29ms)       | `seed.spec.ts:4` — seed                                                                        |
| ✓ PASS (355ms)      | `tab-title.spec.ts:17` — a new tab never displays "Terminal" in its title                      |
| ✘ FAIL (1m timeout) | `tabs.spec.ts:8` — adding a second tab preserves layout with both tabs visible                 |

### Webkit (15 tests)

| Status              | Test                                                                                           |
| ------------------- | ---------------------------------------------------------------------------------------------- |
| ✘ FAIL (1m timeout) | `activity-bell.spec.ts:12` — a bell lights the indicator from inside the alternate buffer      |
| ✘ FAIL (1m timeout) | `activity.spec.ts:10` — a background tab lights the activity indicator on normal-buffer output |
| ✓ PASS (660ms)      | `enhanced-input.spec.ts:10` — read command receives input after enhanced submit                |
| ✓ PASS (618ms)      | `enhanced-input.spec.ts:38` — Ctrl-C at a prompt does not trap input                           |
| ✓ PASS (837ms)      | `enhanced-input.spec.ts:61` — multiple submits in succession all route raw                     |
| ✓ PASS (267ms)      | `seed.spec.ts:4` — seed                                                                        |
| ✓ PASS (740ms)      | `tab-title.spec.ts:17` — a new tab never displays "Terminal" in its title                      |
| ✘ FAIL (1m timeout) | `tabs.spec.ts:8` — adding a second tab preserves layout with both tabs visible                 |
| – SKIPPED           | `clipboard.spec.ts:46` — selecting terminal text copies it to the clipboard                    |
| – SKIPPED           | `clipboard.spec.ts:115` — right-click pastes clipboard text at the cursor                      |
| ✘ FAIL (8.6s)       | `command-editor.spec.ts:18` — editor is visible at the first prompt                            |
| ✘ FAIL (8.6s)       | `command-editor.spec.ts:26` — mouse hit-tests the textarea, not the terminal canvas            |
| ✘ FAIL (8.7s)       | `command-editor.spec.ts:44` — double-click selects a word in the editor                        |

### Blocker #1–#5 reproduction status

| #   | Project  | Test                        | Local result                                    | Same as CI?               |
| --- | -------- | --------------------------- | ----------------------------------------------- | ------------------------- |
| 1   | chromium | `activity.spec.ts:10`       | ✘ timeout — `.tab-add` "element is not visible" | ✅ Same symptom           |
| 2   | chromium | `enhanced-input.spec.ts:61` | ✓ PASS (359ms)                                  | ❌ **Does not reproduce** |
| 3   | chromium | `tabs.spec.ts:8`            | ✘ timeout — `.tab-add` "element is not visible" | ✅ Same symptom           |
| 4   | webkit   | `activity-bell.spec.ts:12`  | ✘ timeout — `.tab-add` "element is not visible" | ✅ Same symptom           |
| 5   | webkit   | `tabs.spec.ts:8`            | ✘ timeout — `.tab-add` "element is not visible" | ✅ Same symptom           |

All four `.tab-add` failures share the identical CI error signature:

```
locator.click: Test timeout of 60000ms exceeded.
  - locator resolved to <button class="tab-add" aria-label="New tab">+</button>
  - attempting click action
    - waiting for element to be visible, enabled and stable
    - element is not visible
```

The page snapshot in every case shows the same DOM: an activity bar with "Connections" and "Sessions" buttons, a "Connections" heading, and a terminal input textbox — **no `.tab` elements exist** even though `expect(page.locator('.tab')).toHaveCount(1)` at line ~13 passes. The `.tab-add` button is rendered in the DOM but has zero-area/invisible layout.

## Step 2 — CSP experiment

**Not run.** Per the brief's stop condition: the local run disagrees with CI on blocker #2, so CSP removal results would not be meaningful.

## Steps 3–4 — n/a

## Other observations

- Server stability note: the Go backend (`nocx-dev-linux-amd64`) stayed alive for the entire ~3.5-minute test run and was confirmed alive after. This is a complete reversal from the Round-1 envinronment failure where WebKit's `WebKitWebProcess` crashed immediately. Mesa/llvmpipe EGL is working.
- All four `.tab-add` failures occur across both engines with the same DOM layout and the same "element is not visible" error, strongly suggesting a CSS/layout issue rather than a runtime crash — same cause, same failure mode. The fact that `.tab-add` resolves but is not visible is consistent with a `display:none` ancestor or `visibility:hidden` chain.

## Files modified

None. `frontend/index.html` confirmed unchanged via `git diff HEAD -- frontend/index.html`. `playwright.config.ts`, `e2e/**`, `frontend/src/**` untouched.

## Process cleanup

- `wails dev` process — killed
- Xvfb (:99) — killed
- Ports :34115, :5173 — verified free

## What could not be verified

1. **Whether CSP causes the 4 `.tab-add` failures.** Blocker #2 passing means a local CSP removal experiment cannot be trusted to match CI behavior. The `.tab-add` invisible-button symptom is CSP-compatible (CSSOM rules from a worker or inline style blocked by `style-src`), but this requires CI-side verification.
2. **The `connect-src` / `default-src` / `worker-src` hypothesis.** Untested for the same reason.

## Recommendation

The four `.tab-add` blockers (#1, #3, #4, #5) reproduced faithfully, and their cause could still be CSP — the symptom (element resolved but invisible) is consistent with a CSS-injecting worker or inline style being blocked. Since blocker #2 (`enhanced-input.spec.ts:61`) did NOT reproduce locally but does fail on CI, the coordinator should:

1. Push a branch commit that removes or relaxes the CSP (or adds the specific blocked directive) and let CI decide.
2. Alternatively, run a targeted CI pipeline that only runs the 5 blocker tests against a CSP-removed commit.
