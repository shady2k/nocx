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

const PLACEMENT_ROW = '.st-row[data-key="tab.placement"]'
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
    // The #tabbar element carries both .tabbar and .tabstrip-vertical
    // classes after a swap — checking for absence of the opposite class
    // is a false negative.  Just verify the expected class is present.
    const wantClass = value === 'vertical' ? 'tabstrip-vertical' : 'tabbar'
    await expect(page.locator('#tabbar')).toHaveClass(new RegExp(wantClass), { timeout: 5000 })
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

    await switchPlacement(page, 'vertical')
    await expect(page.locator(TAB)).toHaveCount(1)

    await switchPlacement(page, 'horizontal')
    await expect(page.locator(TAB)).toHaveCount(1)
    await expect(page.locator(TAB).first()).toHaveClass(/active/)
  })

  // ── Placement visibility (nocx-98z6, nocx-nb8v) ───────────────────────
  // These tests assert actual rendered dimensions, not just class names.
  // The bug was that replaceStrip left the old strip's CSS class on
  // #tabbar, so .tabbar { height:38px; overflow:hidden } clipped vertical
  // tabs to invisible, and .tabstrip-vertical { width:240px } squeezed
  // horizontal tabs.  Class-name assertions (switchPlacement) passed on
  // broken code because both classes were present; only bounding-box checks
  // catch the CSS clipping.
  test('cold start in vertical placement renders tabs with full-height strip', async ({ page }) => {
    // Set vertical placement, close settings, then reload to simulate
    // a cold start that reads the persisted backend setting.
    await switchPlacement(page, 'vertical')
    await page.reload()
    await expect(page.locator('.tab-title').first()).not.toHaveText('', {
      timeout: 10_000,
    })

    // Container must have ONLY the vertical class, not the horizontal one.
    const bar = page.locator('#tabbar')
    await expect(bar).toHaveClass(/tabstrip-vertical/, { timeout: 5000 })
    await expect(bar).not.toHaveClass(/tabbar/)

    // The strip must be a full-height sidebar, not a 38px title bar.
    const barBox = await bar.boundingBox()
    expect(barBox).not.toBeNull()
    expect(barBox!.height).toBeGreaterThan(50)

    // Tab must be visible with a real bounding box.
    const tab = page.locator(TAB).first()
    await expect(tab).toBeVisible()
    const tabBox = await tab.boundingBox()
    expect(tabBox).not.toBeNull()
    expect(tabBox!.height).toBeGreaterThan(20)
    expect(tabBox!.width).toBeGreaterThan(20)
  })

  test('H→V live swap renders every tab with a full-height strip', async ({ page }) => {
    // Start horizontal, add a second tab.
    await page.locator('.tab-add').click()
    await expect(page.locator(TAB)).toHaveCount(2)

    // Switch to vertical — assert the OLD class is gone, not just that
    // the new one arrived.
    await page.keyboard.press('Meta+,')
    await expect(page.locator(PLACEMENT_SELECT)).toBeVisible({ timeout: 5000 })
    await page.selectOption(PLACEMENT_SELECT, 'vertical')
    await page.keyboard.press('Meta+w')
    const bar = page.locator('#tabbar')
    await expect(bar).toHaveClass(/tabstrip-vertical/, { timeout: 5000 })
    await expect(bar).not.toHaveClass(/tabbar/)

    // Container must be taller than 38px — otherwise .tabbar's
    // overflow:hidden is clipping the vertical tabs.
    const barBox = await bar.boundingBox()
    expect(barBox).not.toBeNull()
    expect(barBox!.height).toBeGreaterThan(70) // at least 2 tabs worth

    // The second tab must be within the visible container bounds.
    // If .tabbar survived, the second tab starts at y ≥ 38px and is
    // clipped by the 38px overflow:hidden container.
    const secondTab = page.locator(TAB).nth(1)
    await expect(secondTab).toBeVisible()
    const secondBox = await secondTab.boundingBox()
    expect(secondBox).not.toBeNull()
    expect(secondBox!.height).toBeGreaterThan(20)
    expect(secondBox!.width).toBeGreaterThan(20)
    // Behavioral: Meta+2 must activate the second tab.
    await page.keyboard.press('Meta+2')
    await expect(secondTab).toHaveClass(/active/)
  })

  test('V→H live swap renders every tab with a full-width strip', async ({ page }) => {
    // Switch to vertical first, add a second tab.
    await switchPlacement(page, 'vertical')
    await page.locator('.tab-add').click()
    await expect(page.locator(TAB)).toHaveCount(2)

    // Switch back to horizontal — assert the OLD class is gone.
    await page.keyboard.press('Meta+,')
    await expect(page.locator(PLACEMENT_SELECT)).toBeVisible({ timeout: 5000 })
    await page.selectOption(PLACEMENT_SELECT, 'horizontal')
    await page.keyboard.press('Meta+w')
    const bar = page.locator('#tabbar')
    await expect(bar).toHaveClass(/tabbar/, { timeout: 5000 })
    await expect(bar).not.toHaveClass(/tabstrip-vertical/)

    // Container must be wider than 240px — otherwise .tabstrip-vertical
    // is squeezing the horizontal strip.
    const barBox = await bar.boundingBox()
    expect(barBox).not.toBeNull()
    expect(barBox!.width).toBeGreaterThan(400)

    // Every tab must have a real bounding box.
    for (let i = 0; i < 2; i++) {
      const tab = page.locator(TAB).nth(i)
      await expect(tab).toBeVisible()
      const tabBox = await tab.boundingBox()
      expect(tabBox).not.toBeNull()
      expect(tabBox!.height).toBeGreaterThan(10)
      expect(tabBox!.width).toBeGreaterThan(40)
    }

    // Tabs must be laid out horizontally (second starts to the right of first).
    const firstBox = await page.locator(TAB).nth(0).boundingBox()
    const secondBox = await page.locator(TAB).nth(1).boundingBox()
    expect(firstBox).not.toBeNull()
    expect(secondBox).not.toBeNull()
    expect(secondBox!.x).toBeGreaterThan(firstBox!.x)
    // Behavioral: Meta+2 must activate the second tab.
    await page.keyboard.press('Meta+2')
    await expect(page.locator(TAB).nth(1)).toHaveClass(/active/)
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
