import { test, expect } from './harness'

// Regression guard for the layout regression fixed in 2314a2a: the gutter no
// longer overrides pane.style.position, so multiple tabs don't collapse the
// layout. This test proves that adding a second tab leaves both tabs visible
// and the terminal pane intact.

test('adding a second tab preserves layout with both tabs visible', async ({ page }) => {
  await page.goto('/')

  // Wait for the initial tab to populate its title (session is ready).
  await expect(page.locator('.tab-title').first()).not.toHaveText('', { timeout: 10_000 })
  await expect(page.locator('.tab')).toHaveCount(1)

  // Click the + button to add a second tab.
  await page.locator('.tab-add').click()

  // Both tabs must be present and visible.
  await expect(page.locator('.tab')).toHaveCount(2)
  const tabs = page.locator('.tab')
  await expect(tabs.nth(0)).toBeVisible()
  await expect(tabs.nth(1)).toBeVisible()

  // The active pane must still exist — the layout didn't collapse.
  const pane = page.locator('.pane.active')
  await expect(pane).toBeVisible()

  // The active pane has a non-null bounding box (not zero-area).
  const box = await pane.boundingBox()
  expect(box).not.toBeNull()
  expect(box!.width).toBeGreaterThan(0)
  expect(box!.height).toBeGreaterThan(0)
})

// ── Vertical tab placement (nocx-d3q.4) ────────────────────────────────

const PLACEMENT_ROW = '.ui-settings-row[data-key="tab.placement"]'
const PLACEMENT_SELECT = `${PLACEMENT_ROW} select`
const TAB = '.tab'
const ACTIVITY = '.tab-indicator.tab-activity'

test.describe('vertical tab placement', () => {
  // Reset placement to horizontal before every test so persisted state
  // does not contaminate the next test or the horizontal-only spec above.
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab-title').first()).not.toHaveText('', {
      timeout: 10_000,
    })
    // Open Settings and switch to horizontal.
    await page.keyboard.press('Meta+,')
    await expect(page.locator(PLACEMENT_SELECT)).toBeVisible({
      timeout: 5000,
    })
    await page.selectOption(PLACEMENT_SELECT, 'horizontal')
    await page.keyboard.press('Meta+w')
    await expect(page.locator('#tabbar')).toHaveClass(/tabbar/, {
      timeout: 5000,
    })
  })

  /** Open Settings, switch placement, close Settings, and wait for the
   *  DOM to reflect the new placement. */
  const switchPlacement = async (
    page: import('@playwright/test').Page,
    value: 'horizontal' | 'vertical',
  ) => {
    await page.keyboard.press('Meta+,')
    await expect(page.locator(PLACEMENT_SELECT)).toBeVisible({
      timeout: 5000,
    })
    await page.selectOption(PLACEMENT_SELECT, value)
    await page.keyboard.press('Meta+w')
    // Each orientation mounts into its own host:
    //   horizontal → #tabbar gets .tabbar
    //   vertical   → #vertical-tabstrip gets .tabstrip-vertical
    if (value === 'vertical') {
      await expect(page.locator('#vertical-tabstrip')).toHaveClass(/tabstrip-vertical/, {
        timeout: 5000,
      })
    } else {
      await expect(page.locator('#tabbar')).toHaveClass(/tabbar/, { timeout: 5000 })
    }
  }

  test('tabs render and activate in vertical placement', async ({ page }) => {
    await switchPlacement(page, 'vertical')

    await expect(page.locator(TAB)).toHaveCount(1)
    const tab = page.locator(TAB).first()
    await expect(tab).toBeVisible()
    await expect(tab).toHaveClass(/active/)

    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)

    await page.keyboard.press('Meta+1')
    await expect(page.locator(TAB).first()).toHaveClass(/active/)

    await page.keyboard.press('Meta+2')
    await expect(page.locator(TAB).nth(1)).toHaveClass(/active/)
  })

  test('activity indicator lights for a backgrounded tab in vertical placement', async ({
    page,
  }) => {
    await switchPlacement(page, 'vertical')

    // Click the pane to ensure keyboard focus lands on the terminal
    // editor after the settings tab closes.  JS focus() alone may not
    // route subsequent page.keyboard.type() to the PTY.
    const paneBox = await page.locator('.pane.active').boundingBox()
    await page.mouse.click(paneBox!.x + paneBox!.width / 2, paneBox!.y + paneBox!.height - 30)

    await page.keyboard.type('sleep 3; echo PROBE')
    await page.keyboard.press('Enter')

    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)
    await expect(page.locator(TAB).first()).not.toHaveClass(/active/)
    await expect(page.locator(TAB).first().locator(ACTIVITY)).toBeAttached({ timeout: 15000 })
  })

  test('switching placement repositions the strip without a restart', async ({ page }) => {
    // beforeEach already reset to horizontal.
    await expect(page.locator('#tabbar')).toHaveClass(/tabbar/)

    const paneHandle = await page.evaluateHandle(() => document.querySelector('.pane.active'))

    await switchPlacement(page, 'vertical')
    await expect(page.locator(TAB)).toHaveCount(1)

    // Verify the terminal host is the same DOM node.
    // ADR-0012 §1: must not remount the terminal host element.
    const same = await page.evaluate((handle) => {
      const active = document.querySelector('.pane.active')
      return active === handle
    }, paneHandle)
    expect(same).toBe(true)

    await switchPlacement(page, 'horizontal')
    await expect(page.locator(TAB)).toHaveCount(1)
    await expect(page.locator(TAB).first()).toHaveClass(/active/)
  })

  // Reset placement to horizontal after each vertical test so the
  // persisted backend setting does not contaminate other test files
  // or the next full-suite run.  beforeEach also resets, but this is
  // the last line of defense — it runs even if the test body fails.
  test.afterEach(async ({ page }) => {
    await page.keyboard.press('Meta+,')
    await expect(page.locator(PLACEMENT_SELECT)).toBeVisible({
      timeout: 5000,
    })
    await page.selectOption(PLACEMENT_SELECT, 'horizontal')
    await page.keyboard.press('Meta+w')
    await expect(page.locator('#tabbar')).toHaveClass(/tabbar/, {
      timeout: 5000,
    })
  })
})
