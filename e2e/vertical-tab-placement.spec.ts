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
    // Settings opens on its FIRST section, so a row in any other section is in
    // the DOM and hidden. Navigate before waiting, or this times out on a
    // control that is right there and reads like a broken selector.
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
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
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })
    // Ensure horizontal before every test
    await switchPlacement(page, 'horizontal')
  })

  test('the activity bar keeps the trailing edge in both placements (nocx-crjft)', async ({
    page,
  }) => {
    // The invariant, asserted where a user could see it broken: whatever the
    // tab placement, the rail is the last thing across the window and the
    // panes come before it. Checked in BOTH placements because the whole
    // point of the move is that the shell does NOT rearrange itself when the
    // placement changes — an assertion in one placement would pass for a
    // shell that swaps sides in the other.
    //
    // It replaces "the strip sits between the activity bar and the panes",
    // which pinned the arrangement this bead reverses: the rail had the
    // leading edge on the argument that it is app-level chrome, and three of
    // the six panel views render for whatever tab is in front, so the panel
    // sat upstream of the thing that selects the tab.
    for (const placement of ['vertical', 'horizontal'] as const) {
      await switchPlacement(page, placement)

      const barBox = await page.locator('#activitybar').boundingBox()
      const panesBox = await page.locator('#panes').boundingBox()
      expect(barBox, placement).not.toBeNull()
      expect(panesBox, placement).not.toBeNull()

      // The rail is past the panes...
      expect(barBox!.x, placement).toBeGreaterThanOrEqual(panesBox!.x + panesBox!.width - 1)
      // ...and the panes still have room (not collapsed by a misplaced strip).
      expect(panesBox!.width, placement).toBeGreaterThan(0)

      if (placement === 'vertical') {
        // The strip lists the panes and now owns the leading edge.
        const stripBox = await page.locator('#vertical-tabstrip').boundingBox()
        expect(stripBox, placement).not.toBeNull()
        expect(stripBox!.x + stripBox!.width, placement).toBeLessThanOrEqual(panesBox!.x + 1)
        // Below the drag bar (not at y=0 inside #tabbar).
        expect(stripBox!.y, placement).toBeGreaterThanOrEqual(30)
      }
    }
  })

  test('the strip actions sit above the tab list, together (nocx-82l9.3)', async ({ page }) => {
    await switchPlacement(page, 'vertical')

    // Both actions belong to one group at the top of the column. They used to be
    // loose siblings of the list, which is `flex: 1 1 auto` — so it pushed them
    // apart and stranded the caret alone in the bottom corner.
    //
    // The caret is now the strip's MENU (`aria-label="More"`): the rework cut
    // the row's five same-weight marks to three and made Quick connect a named
    // row underneath it (tab-strip.tsx). Which mark it is does not change what
    // this test is about — that the actions stay together, above the list.
    const strip = page.locator('#vertical-tabstrip')
    const plus = await strip.locator('[aria-label="New tab"]').boundingBox()
    const caret = await strip.locator('[aria-label="More"]').boundingBox()
    const firstPane = await strip.locator('.nocx-tab').first().boundingBox()

    expect(plus!.y + plus!.height).toBeLessThanOrEqual(firstPane!.y + 1)
    // On one line with each other, not at opposite ends of the column.
    expect(Math.abs(plus!.y - caret!.y)).toBeLessThanOrEqual(2)
  })

  test('title font-size is the same in both orientations', async ({ page }) => {
    // Measure in vertical
    await switchPlacement(page, 'vertical')
    const verticalTitle = page.locator('.tabstrip-vertical .nocx-tab-title').first()
    const vz = await verticalTitle.boundingBox()
    expect(vz).not.toBeNull()

    // Measure in horizontal
    await switchPlacement(page, 'horizontal')
    const horizontalTitle = page.locator('.tabbar .nocx-tab-title').first()
    const hz = await horizontalTitle.boundingBox()
    expect(hz).not.toBeNull()

    // The title's height (derived from font-size + line-height) is the same
    // in both orientations because the title sets its own font-size: var(--font-size-sm).
    expect(hz!.height).toBe(vz!.height)
  })

  test('row height stays fixed (52px) when subtitle appears', async ({ page }) => {
    await switchPlacement(page, 'vertical')

    // Add a second tab that will have no tooltip initially
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.locator('.nocx-tab')).toHaveCount(2)

    // Both rows should have the same height (fixed at 52px)
    const firstPane = page.locator('.nocx-tab').nth(0)
    const secondTab = page.locator('.nocx-tab').nth(1)
    const firstBox = await firstPane.boundingBox()
    const secondBox = await secondTab.boundingBox()
    expect(firstBox).not.toBeNull()
    expect(secondBox).not.toBeNull()
    expect(firstBox!.height).toBe(52)
    expect(secondBox!.height).toBe(52)
  })

  test.afterEach(async ({ page }) => {
    // Reset to horizontal so persisted state doesn't affect other tests
    await switchPlacement(page, 'horizontal')
  })
})
