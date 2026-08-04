/**
 * e2e: sandboxed-shell action (ADR-0019 §3).
 *
 * Walks the Quick Connect picker and the Settings toggle: the action is a
 * capability/visibility gate, so flipping the flag must only change what the
 * picker offers — ordinary local tabs, the initial tab and the picker itself
 * behave identically before and after.
 *
 * The full picker→tab flow needs a native directory picker, which exists only
 * inside Wails; the headless harness has no Wails at all, so dialog.openDirectory
 * is -32601 there and this spec stops at the action surface. The backend-side
 * guarantees (a failed sandbox setup creates no registered session/tab, no
 * unsandboxed fallback) are asserted at the transport layer (ws_sandbox_test.go)
 * and by the enforcement smokes (make sandbox-smoke-linux / -macos).
 *
 * The settings toggle is driven through the control's change event: on the
 * headless (vite + chromium) path the settings pane renders with zero layout
 * (a pre-existing environment artifact wails dev does not have), so a pointer
 * click is impossible, while the settings.set RPC, persistence, broadcast and
 * the live snapshot read the picker depends on all work normally. The picker
 * assertions below are the verdict: they only pass when the flag actually
 * changed server-side.
 */
import { test, expect } from './harness'

const CARET = '[aria-label="Quick connect"]'
const QUICK_CONNECT_ITEM = '.quick-connect__item'
const QUICK_CONNECT_LIST = '.quick-connect__list'
const SANDBOX_ACTION = '.quick-connect__item:has-text("Sandboxed shell")'
const SANDBOX_UNAVAILABLE = '.quick-connect__item:has-text("Sandbox unavailable")'

async function openSettings(page: import('@playwright/test').Page): Promise<void> {
  const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
  await page.keyboard.press(`${mod}+,`)
  await expect(page.getByRole('searchbox', { name: 'Search settings' })).toBeVisible()
}

/** Flip sandbox.enabled to `on` through the real settings wire. */
async function setSandboxFlag(page: import('@playwright/test').Page, on: boolean): Promise<void> {
  await openSettings(page)
  await page.evaluate((checked) => {
    const el = document.querySelector<HTMLInputElement>(
      '[id="st-setting-sandbox.enabled"] input[type="checkbox"]',
    )
    if (!el) throw new Error('sandbox.enabled toggle not found')
    if (el.checked !== checked) {
      el.checked = checked
      el.dispatchEvent(new Event('change', { bubbles: true }))
    }
  }, on)
  // The persisted value is what the picker reads; give the settings.set RPC
  // and the settings.changed broadcast a moment to settle before asserting
  // the picker's live snapshot.
  await page.waitForTimeout(500)
}

test.describe('sandboxed shell action', () => {
  // The flag workflow walks Settings and the picker several times — heavier
  // than a single-surface spec, so it gets a generous budget on slow runners.
  test.setTimeout(120_000)

  test('flag off: no sandbox action, ordinary flows unchanged', async ({ page }) => {
    await page.goto('/')
    // The initial tab is the app's own cold start; a slow runner (headless
    // vite+devharness) can take longer than the 5s default, so the boot wait
    // gets a generous bound — the assertion still requires the tab.
    await expect(page.getByRole('tab')).toHaveCount(1, { timeout: 20000 })

    // Deterministic start: the flag is OFF (the default; the e2e backend
    // persists settings across runs, so the previous state is not trusted).
    await setSandboxFlag(page, false)

    // The picker offers exactly the ordinary actions while the flag is off.
    await page.locator(CARET).click()
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()
    await expect(page.locator(QUICK_CONNECT_ITEM).first()).toContainText('Local shell')
    await expect(page.locator(SANDBOX_ACTION)).toHaveCount(0)
    await expect(page.locator(SANDBOX_UNAVAILABLE)).toHaveCount(0)

    // Ordinary local tab still opens.
    const before = await page.getByRole('tab').count()
    await page.keyboard.press('Enter')
    await expect(page.getByRole('tab')).toHaveCount(before + 1)
  })

  test('flag on: exactly one sandbox row renders (action or unavailable), and disabling hides it again', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1, { timeout: 20000 })

    // Enable the experimental flag in Settings.
    await setSandboxFlag(page, true)

    // The picker now shows exactly one sandbox row: the action when the
    // backend reports available, the typed-unavailable row otherwise. Which
    // one depends on the machine (Landlock/Seatbelt availability) — the
    // contract is that exactly one exists and the ordinary actions stay.
    await page.locator(CARET).click()
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()
    const actionCount = await page.locator(SANDBOX_ACTION).count()
    const unavailableCount = await page.locator(SANDBOX_UNAVAILABLE).count()
    expect(actionCount + unavailableCount).toBe(1)
    if (unavailableCount === 1) {
      // The row names the typed reason.
      await expect(page.locator(SANDBOX_UNAVAILABLE)).toContainText(
        /landlock|seatbelt|unsupported|probe|sandbox-exec/,
      )
    }

    // Ordinary flows are untouched by the flag.
    const before = await page.getByRole('tab').count()
    await page.keyboard.press('Enter')
    await expect(page.getByRole('tab')).toHaveCount(before + 1)

    // Disabling the flag removes the sandbox row from the next open.
    await setSandboxFlag(page, false)
    await page.locator(CARET).click()
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()
    await expect(page.locator(SANDBOX_ACTION)).toHaveCount(0)
    await expect(page.locator(SANDBOX_UNAVAILABLE)).toHaveCount(0)
  })
})
