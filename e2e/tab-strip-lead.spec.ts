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

// THE NOTICES ARE CONDITIONS, AND A CONDITION THAT IS NOT TRUE TAKES NO ROOM.
// `.update-notice` and `.connection-notice` are the strip's first two children.
// Both carry `hidden`, and both declared `display: flex` — which beats the UA
// stylesheet's `[hidden] { display: none }`, so neither was hidden at all: two
// empty flex boxes with `padding: 0 12px` owned 48px of the row at all times,
// on every platform. On macOS that landed immediately after the traffic-light
// inset, which is where it was noticed.
test('a hidden notice takes no width in the tab strip', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(BAR)).toBeVisible()

  // The precondition, stated rather than assumed: this asserts nothing about
  // hiding if the notices are showing.
  await expect(page.locator('.update-notice')).toBeHidden()
  await expect(page.locator('.connection-notice')).toBeHidden()

  const widths = await page.evaluate(() =>
    [...(document.querySelector('.tabbar') as HTMLElement).children]
      .filter((c) => c.querySelector('.update-notice, .connection-notice'))
      .map((c) => c.getBoundingClientRect().width),
  )
  expect(widths.length).toBeGreaterThan(0)
  for (const w of widths) expect(w).toBe(0)
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
