import { test, expect } from './harness'

/**
 * e2e: what stands between the window's edge and the first control of the tab
 * strip (nocx-gm4fj).
 *
 * Measured in a browser and not asserted in jsdom, because both halves of the
 * defect are layout: an attribute that a `display` declaration overrode, and a
 * padding calibrated for a title bar the app stopped asking for. Neither is
 * visible to a test that reads the DOM.
 */

const BAR = '.tabbar'
const LEAD = '.tabbar .tabstrip-lead'

/** Left edge of a box, relative to the strip's own left edge. */
async function leftWithin(
  page: import('@playwright/test').Page,
  selector: string,
): Promise<number> {
  return page.evaluate((sel) => {
    const bar = document.querySelector('.tabbar') as HTMLElement
    const el = document.querySelector(sel) as HTMLElement
    return el.getBoundingClientRect().left - bar.getBoundingClientRect().left
  }, selector)
}

// The update notice is the only optional child before the tab strip lead.
// Connection state now owns a top-layer overlay, so it must not add a second
// child to the tab bar or participate in this layout contract.
test('the tab strip lead follows the update notice without another condition child', async ({
  page,
}) => {
  await page.goto('/')
  await expect(page.locator(BAR)).toBeVisible()
  await expect(page.locator('.update-notice')).toBeHidden()
  await expect(page.locator(LEAD)).toBeVisible()

  const roles = await page.evaluate(() =>
    [...(document.querySelector('.tabbar') as HTMLElement).children].map((child) => {
      if (child.querySelector('.update-notice')) return 'update'
      if (child.querySelector('.tabstrip-lead')) return 'lead'
      return 'other'
    }),
  )
  expect(roles.slice(0, 2)).toEqual(['update', 'lead'])
})

// AND THEREFORE the first control sits exactly where the platform's window
// controls leave off — nothing else may accumulate in front of it. Off macOS
// there are no window controls in the content area, so the inset is zero and
// the lead starts at the strip's own edge.
test('the first control starts at the platform inset and not past it', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(LEAD)).toBeVisible()

  const inset = (p: string) =>
    page.evaluate((plat) => {
      document.documentElement.setAttribute('data-platform', plat)
      return getComputedStyle(document.documentElement).getPropertyValue('--titlebar-inset-start')
    }, p)

  await page.evaluate(() => document.documentElement.setAttribute('data-platform', 'linux'))
  expect(await leftWithin(page, LEAD)).toBe(0)

  // On macOS the strip reserves the traffic lights and nothing more. They end
  // at ~59.5 CSS px with `MacTitleBarHidden` (7px + two 20px steps + a 12px
  // button), so the reserved run has to clear that and stay close to it.
  const darwin = parseFloat(await inset('darwin'))
  expect(darwin).toBeGreaterThanOrEqual(60)
  expect(darwin).toBeLessThanOrEqual(72)
  expect(await leftWithin(page, LEAD)).toBe(darwin)
})
