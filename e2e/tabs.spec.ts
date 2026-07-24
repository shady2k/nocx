import { test, expect } from '@playwright/test'

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
