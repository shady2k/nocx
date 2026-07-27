import { test, expect } from './harness'

test.describe('settings scroll — normal', () => {
  // Bug: the .ui-page__scroll scroll chain is broken because SettingsContent.mount
  // appends an unclassed <div> into .pane, so flex:1 on .ui-page__scroll never
  // receives a bounded block size and the pane clips the content instead of
  // scrolling it. Reproduces only in WebKit (nocx-82l9.2).

  test.use({ viewport: { width: 1024, height: 520 } })

  test('scrolls the last setting row into view in a short window (nocx-82l9.2)', async ({
    page,
    browserName,
  }) => {
    test.skip(browserName !== 'webkit', 'Settings scroll bug is WebKit-only (nocx-82l9.2)')

    await page.goto('/')
    await expect(page.locator('.tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Find the last visible row and scroll it into view
    const lastRow = page.locator('.ui-settings-row').last()
    await lastRow.scrollIntoViewIfNeeded()

    // When the scroll chain works, the last row's bottom is within the pane.
    // With the bug, the pane clips the content and scrolling has no effect.
    const pane = page.locator('.pane.active')
    const paneBox = await pane.boundingBox()
    const rowBox = await lastRow.boundingBox()

    expect(rowBox).not.toBeNull()
    expect(paneBox).not.toBeNull()
    expect(rowBox!.y + rowBox!.height).toBeLessThanOrEqual(paneBox!.y + paneBox!.height)
  })
})

test.describe('settings scroll — narrow', () => {
  test.use({ viewport: { width: 600, height: 520 } })

  test('scrolls the last setting row in narrow (stacked, <640px) layout', async ({
    page,
    browserName,
  }) => {
    test.skip(browserName !== 'webkit', 'Settings scroll bug is WebKit-only (nocx-82l9.2)')

    await page.goto('/')
    await expect(page.locator('.tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Narrow layout stacks rail above content — verify scroll still works.
    const lastRow = page.locator('.ui-settings-row').last()
    await lastRow.scrollIntoViewIfNeeded()

    const pane = page.locator('.pane.active')
    const paneBox = await pane.boundingBox()
    const rowBox = await lastRow.boundingBox()

    expect(rowBox).not.toBeNull()
    expect(paneBox).not.toBeNull()
    expect(rowBox!.y + rowBox!.height).toBeLessThanOrEqual(paneBox!.y + paneBox!.height)
  })
})
