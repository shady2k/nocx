import { expect, promptReady, test } from './harness'

/**
 * PROOF SPEC for nocx-n14oo.11 — deliberately fails, to demonstrate the
 * failure-context block a real failure now prints.
 *
 * NOT part of the ordinary suite. It exists to be read, not to pass: it
 * asserts something the app never does, on purpose, so the harness's
 * failure-context fixture (e2e/failure-context.ts, wired into the `page`
 * fixture in e2e/harness.ts) runs its report and prints, to stderr, at the
 * moment of failure:
 *
 *   - the shared stand's backend log lines carrying THIS test's own
 *     trace_id (e2e/trace-context.ts derives it from testInfo.testId; the
 *     renderer sends it as `traceparent` on its WebSocket connection, and
 *     internal/transport/ws.go continues it into every line the connection
 *     causes),
 *   - the browser's console messages (one is emitted below on purpose, so
 *     the section is never empty even on a quiet page),
 *   - the control-plane's own JSON-RPC frames (method, id, error — the
 *     bootstrap alone produces several: layout, session and UI state reads),
 *   - the page's accessibility snapshot.
 *
 * Gated behind an env var so CI's default run never sees a test written to
 * fail. To run it and read the block:
 *
 *   NOCX_PROVE_FAILURE_CONTEXT=1 npx playwright test e2e/failure-context-proof.spec.ts
 *
 * or, in the container:
 *
 *   NOCX_PROVE_FAILURE_CONTEXT=1 PW_PROJECTS=chromium e2e/run-in-container.sh e2e/failure-context-proof.spec.ts
 */
test.skip(
  process.env.NOCX_PROVE_FAILURE_CONTEXT !== '1',
  'proof-only spec for nocx-n14oo.11; set NOCX_PROVE_FAILURE_CONTEXT=1 to run it',
)

test('deliberately fails, to print the failure-context block', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  // A console line the failure block is guaranteed to carry, so the proof
  // does not depend on whatever the app happens to log on a quiet boot.
  await page.evaluate(() => {
    console.log('nocx-n14oo.11 proof: this line should appear in the printed console section')
  })

  // Deliberately false: a fresh profile always has exactly one tab
  // (harness.ts's resetStand), so asserting 999 fails every time, on
  // purpose, without depending on any other property of the app.
  await expect(page.locator('.nocx-tab')).toHaveCount(999)
})
