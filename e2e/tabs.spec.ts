import { test, expect, clickIntoEditor } from './harness'

// Regression guard for the layout regression fixed in 2314a2a: the gutter no
// longer overrides pane.style.position, so multiple tabs don't collapse the
// layout. This test proves that adding a second tab leaves both tabs visible
// and the terminal pane intact.

test('adding a second tab preserves layout with both tabs visible', async ({ page }) => {
  await page.goto('/')

  // Wait for the initial tab to populate its title (session is ready).
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })
  await expect(page.locator('.nocx-tab')).toHaveCount(1)

  // Click the + button to add a second tab.
  await page.locator('[aria-label="New tab"]').click()

  // Both tabs must be present and visible.
  await expect(page.locator('.nocx-tab')).toHaveCount(2)
  const tabs = page.locator('.nocx-tab')
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
const TAB = '.nocx-tab'
const ACTIVITY = '.nocx-tab-indicator[data-activity="true"]'

test.describe('vertical tab placement', () => {
  // Reset placement to horizontal before every test so persisted state
  // does not contaminate the next test or the horizontal-only spec above.
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', {
      timeout: 10_000,
    })
    // Open Settings, go to the section that owns the setting, and switch to
    // horizontal. The rail navigation is not optional: Settings opens on its
    // FIRST section rather than listing every setting end to end, so a row in
    // any other section is present in the DOM and hidden. Waiting for it to
    // become visible without navigating times out on a control that is right
    // there — which reads like a broken selector and is not one.
    await page.keyboard.press('Meta+,')
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
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
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
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
    await expect(tab).toHaveAttribute('aria-selected', 'true')

    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)

    await page.keyboard.press('Meta+1')
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'true')

    await page.keyboard.press('Meta+2')
    await expect(page.locator(TAB).nth(1)).toHaveAttribute('aria-selected', 'true')
  })

  /**
   * nocx-zudj: with two tabs open, switching to vertical left the strip listing no tabs
   * at all — the swap mounted the new strip without repopulating its display records.
   * The test above cannot catch it: it switches with ONE tab and adds the second while
   * already vertical, so the populate path it exercises is addTab and not replaceStrip.
   *
   * Asserted by measured geometry rather than by presence: a strip with rows that exist
   * at zero height would satisfy any assertion about the DOM while showing nothing (the
   * lesson recorded on nocx-d3q).
   *
   * The original tell was the New-tab + sitting at the vertical MIDDLE, because an empty
   * `.tabs-container` (flex: 1 1 auto) split the column. That reading is gone: the strip's
   * actions now sit in a fixed row at the TOP, so the + no longer moves with the list and
   * says nothing about it. What replaced it measures the list directly — the rows must
   * fill the column downward from the actions, which an empty container cannot fake.
   */
  test('switching to vertical with two tabs open lists both of them (nocx-zudj)', async ({
    page,
  }) => {
    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)

    await switchPlacement(page, 'vertical')

    const strip = page.locator('#vertical-tabstrip')
    await expect(strip.locator(TAB)).toHaveCount(2)

    const first = await strip.locator(TAB).first().boundingBox()
    const second = await strip.locator(TAB).nth(1).boundingBox()
    const add = await strip.locator('[aria-label="New tab"]').boundingBox()
    const stripBox = await strip.boundingBox()

    // Both rows have real height and stack down the column.
    expect(first!.height).toBeGreaterThan(10)
    expect(second!.height).toBeGreaterThan(10)
    expect(second!.y).toBeGreaterThan(first!.y)
    // The list starts under the actions row and runs down the strip, which is what
    // an empty container cannot do however many records claim to exist.
    expect(first!.y).toBeGreaterThanOrEqual(add!.y + add!.height)
    expect(first!.x).toBeGreaterThanOrEqual(stripBox!.x)
    expect(second!.y + second!.height).toBeLessThanOrEqual(stripBox!.y + stripBox!.height)
  })

  test('activity indicator lights for a backgrounded tab in vertical placement', async ({
    page,
  }) => {
    await switchPlacement(page, 'vertical')

    // Click the editor to ensure keyboard focus lands on it after the settings
    // tab closes.  JS focus() alone may not route subsequent
    // page.keyboard.type() to the PTY.
    await clickIntoEditor(page)

    await page.keyboard.type('sleep 3; echo PROBE')
    await page.keyboard.press('Enter')

    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'false')
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
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'true')
  })

  /**
   * The second line is not "the tooltip, if any" — it is the tab's location, shown only
   * when the first line is a NAME rather than that same location. A plain local tab is
   * titled after its directory and its tooltip is that directory, so printing both would
   * be the first line twice. Both directions are asserted here because the rule is the
   * whole point: presence alone passed on the version that showed the duplicate.
   */
  test('the second line appears only when the title is a name, not the directory', async ({
    page,
  }) => {
    await switchPlacement(page, 'vertical')

    // A freshly opened local tab: the title is the directory, so no second line.
    await expect(page.locator(TAB)).toHaveCount(1)
    await expect(page.locator('.nocx-tab-subtitle')).toHaveCount(0)

    // Give the tab a name of its own via OSC 0. Now the location is extra information
    // and earns its line.
    await clickIntoEditor(page)
    const marker = `NAME-${Date.now().toString(36)}`
    await page.keyboard.type(`printf '\\033]0;${marker}\\007'`)
    await page.keyboard.press('Enter')

    await expect(page.locator('.nocx-tab-title').first()).toHaveText(marker, { timeout: 5000 })
    const subtitle = page.locator('.nocx-tab-subtitle').first()
    await expect(subtitle).toBeAttached({ timeout: 5000 })
    await expect(subtitle).not.toHaveText('')
  })

  test('vertical label text starts near the left edge (not centred)', async ({ page }) => {
    await switchPlacement(page, 'vertical')

    const tab = page.locator(TAB).first()
    const title = tab.locator('.nocx-tab-title')
    const tabBox = await tab.boundingBox()
    const titleBox = await title.boundingBox()

    // Title's left edge should be near the tab's left content edge:
    // 10px tab padding + 10px pill left + 22px pill width + 10px gap = 52px.
    // This is well left of centre (which would be ~80+ px for a 240px strip).
    expect(titleBox!.x - tabBox!.x).toBeLessThan(60)
  })

  test('horizontal label text is centred (not near the left edge)', async ({ page }) => {
    // beforeEach already reset to horizontal, but make sure.
    await expect(page.locator('#tabbar')).toHaveClass(/tabbar/)

    const tab = page.locator(TAB).first()
    const title = tab.locator('.nocx-tab-title')
    const tabBox = await tab.boundingBox()
    const titleBox = await title.boundingBox()

    // In horizontal with centering, the title's left edge should be well
    // past 40px from the tab's left edge (centered text in a 200px tab).
    expect(titleBox!.x - tabBox!.x).toBeGreaterThan(40)
  })

  // Reset placement to horizontal after each vertical test so the
  // persisted backend setting does not contaminate other test files
  // or the next full-suite run.  beforeEach also resets, but this is
  // the last line of defense — it runs even if the test body fails.
  test.afterEach(async ({ page }) => {
    await page.keyboard.press('Meta+,')
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
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
