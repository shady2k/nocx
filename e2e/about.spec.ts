import { test, expect, promptReady } from './harness'

/**
 * e2e: the app can say what build it is (nocx-8bbp).
 *
 * The task the page exists for, watched end to end through the real backend:
 * open Settings, find About, and read the build out. Before this the settings
 * rail had Clipboard, Interface, Export/Backup/Import and Connections, and a
 * person filing a bug had nothing to read and nothing to quote.
 *
 * The values are asserted as SHAPES, not as literals: what a dev stand reports
 * for its commit and its Go version is not what a release reports, and a test
 * pinned to either would be asserting which machine it ran on.
 */

const RAIL_ABOUT = '.ui-grouped-nav__item[data-item="about"] button'

test('Settings has an About page that says what build this is', async ({ page }) => {
  await page.goto('/')
  // Up before the shortcut is pressed: a Meta+, that lands before the binding
  // is wired opens nothing, and the wait after it then burns the whole budget
  // on an element that is never coming (snippets.spec.ts records the same).
  await promptReady(page)
  await page.keyboard.press('Meta+,')
  await page.locator(RAIL_ABOUT).click()

  // The identity block: the app's own icon, its name, and the version.
  const icon = page.locator('.ab-icon')
  await expect(icon).toBeVisible({ timeout: 20_000 })
  // Visible is not loaded — a broken src still lays out a box. The image has to
  // have decoded, or "shows the icon" is a claim about an alt-text placeholder.
  await expect
    .poll(() => icon.evaluate((el) => (el as HTMLImageElement).naturalWidth), {
      timeout: 10_000,
      message: 'the app icon never decoded',
    })
    .toBeGreaterThan(0)
  await expect(page.getByText('nocx', { exact: true }).first()).toBeVisible()

  // Every row carries a value. The point of the page is that nothing on it is
  // blank: a row that has not loaded and a row with nothing in it look the
  // same, so the backend spells absence as a word.
  const values = await page.locator('.ab-rows dd').allTextContents()
  expect(values.length).toBeGreaterThanOrEqual(5)
  for (const v of values) expect(v.trim()).not.toBe('')

  // An unstamped stand says so rather than presenting "dev" as a release. This
  // IS the dev stand, so the mark must be there — the release case is asserted
  // in the component's own tests, where the build can be chosen.
  await expect(page.getByText(/development build/i)).toBeVisible()
})

// ONE ACTION, because the reason anybody opens this page is to paste it
// somewhere. Read back through the clipboard the app itself writes to.
test.describe('Copy diagnostics', () => {
  // The same bound clipboard.spec.ts is under, for the same reason and in the
  // same words: reading the clipboard back needs a permission only Chromium
  // implements. The page itself is asserted on both browsers by the test above
  // — this one is about the round trip.
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'clipboard-read permission is Chromium-only; WebKit must be checked manually',
  )

  test('puts the whole block on the clipboard', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.goto('/')
    await promptReady(page)
    await page.keyboard.press('Meta+,')
    await page.locator(RAIL_ABOUT).click()
    await expect(page.locator('.ab-rows dd').first()).toBeVisible({ timeout: 20_000 })

    const shown = await page.locator('.ab-rows dd').allTextContents()
    await page.getByRole('button', { name: 'Copy diagnostics' }).click()

    const copied = await page.evaluate(() => navigator.clipboard.readText())
    expect(copied).toContain('nocx')
    for (const value of shown) expect(copied).toContain(value.trim())
    // And the version, which the list deliberately does not repeat because the
    // headline beside the icon already says it — the copy carries both.
    const version = (await page.locator('.ab-version').textContent())?.trim() ?? ''
    expect(version).not.toBe('')
    expect(copied).toContain(version)
  })
})
