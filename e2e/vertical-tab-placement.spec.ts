import { test, expect, type Page } from './harness'

test.describe('vertical tab placement', () => {
  // Fixed: #vertical-tabstrip container now exists alongside #body.
  // The strip mounts into #vertical-tabstrip in vertical placement and
  // into #tabbar in horizontal placement. #tabbar stays present as the
  // Wails drag region in both placements (nocx-82l9.3).

  // Shared helpers — copied from tabs.spec.ts conventions (that file is
  // owned by nocx-82l9.3 and must not be edited here).
  const PLACEMENT_ROW = '.ui-settings-row[data-key="tab.placement"]'
  const PLACEMENT_SELECT = `${PLACEMENT_ROW} select`

  async function switchPlacement(page: Page, value: 'horizontal' | 'vertical'): Promise<void> {
    await page.keyboard.press('Meta+,')
    await expect(page.locator(PLACEMENT_SELECT)).toBeVisible({ timeout: 5000 })
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

  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab-title').first()).not.toHaveText('', { timeout: 10_000 })
    // Ensure horizontal before every test
    await switchPlacement(page, 'horizontal')
  })

  test('vertical tab strip sits left of #body below the drag bar (nocx-82l9.3)', async ({
    page,
  }) => {
    await switchPlacement(page, 'vertical')

    // The vertical tab strip is in #vertical-tabstrip. Tab buttons should
    // sit left of #body and below the drag bar.
    const firstTab = page.locator('.tab').first()
    const body = page.locator('#body')
    const panes = page.locator('#panes')

    await expect(firstTab).toBeVisible()
    const tabBox = await firstTab.boundingBox()
    const bodyBox = await body.boundingBox()
    const panesBox = await panes.boundingBox()

    expect(tabBox).not.toBeNull()
    expect(bodyBox).not.toBeNull()
    expect(panesBox).not.toBeNull()

    // Tab strip must sit LEFT of #body (not inside the top bar)
    expect(tabBox!.x + tabBox!.width).toBeLessThanOrEqual(bodyBox!.x)
    // Tab strip must sit BELOW the drag bar (not at y=0 inside #tabbar)
    expect(tabBox!.y).toBeGreaterThanOrEqual(30)
    // #panes must keep non-zero width (not collapsed by misplaced strip)
    expect(panesBox!.width).toBeGreaterThan(0)
  })

  test.afterEach(async ({ page }) => {
    // Reset to horizontal so persisted state doesn't affect other tests
    await switchPlacement(page, 'horizontal')
  })
})
