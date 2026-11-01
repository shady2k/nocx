import { test, expect } from './harness'

/**
 * e2e: the tab strip carries no update offer, and its lead is the first control
 * (nocx-gm4fj, rewritten for nocx-1ebrs.66.1.5).
 *
 * WHAT CHANGED. The update offer used to live in the tab strip, and these two
 * tests protected the layout from it: a hidden `.update-notice` must reserve no
 * width, and the strip's lead had to follow it without another condition child
 * sneaking in between. The owner removed the offer from the strip on 2026-10-26
 * — it took room from open tabs — and put it on the activity bar's bottom group
 * instead. The requirement these tests encode is therefore a different one, and
 * it is stronger rather than absent: the strip must not carry an update child AT
 * ALL, so a future change cannot quietly reintroduce one.
 *
 * Kept in a browser rather than jsdom because both of the defects they were
 * written against were layout defects (`.update-notice`'s `display: flex`
 * overriding `[hidden]`), and the new assertions are about what the strip is
 * made of, which only the mounted shell can answer.
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

test('the tab strip carries no update offer at all', async ({ page }) => {
  await page.goto('/')
  // THE STRIP IS NOT IN THE FIRST PAINT, AND `.tabbar` IS NOT EVIDENCE THAT IT
  // IS (nocx-1ebrs.66.1.31). `.tabbar` is static markup in the shell
  // (frontend/src/App.tsx:34); the strip mounts into it when the socket
  // connects, and the activity bar in the same generation
  // (frontend/src/main.tsx:1530, `mountConnectedSidebar`). This test used to
  // wait for the tabbar and then read both surfaces once. On a loaded runner
  // that read the empty shell: the update assertions below held vacuously
  // while the seam's failed with `Expected: 1, Received: 0`, and the page's
  // own snapshot still said "Connecting to nocx…" (CI run 40884397691).
  // Readiness is therefore asked of the two things this test is about, and
  // waited for the way the rest of this file waits for anything.
  await expect(page.locator(LEAD)).toBeVisible()
  await expect(page.locator('.activity-bar-bottom')).toHaveCount(1)

  // The offer lives on the activity bar now; the strip must not have one —
  // not visible, not hidden, not reserving width. `count` rather than
  // `toBeHidden` is deliberate: a hidden node is exactly what used to reserve
  // 48px here.
  await expect(page.locator('.update-notice')).toHaveCount(0)
  const stripChildrenWithUpdate = await page.evaluate(
    () =>
      [...(document.querySelector('.tabbar') as HTMLElement).children].filter(
        (child) => child.querySelector('.update-notice') !== null,
      ).length,
  )
  expect(stripChildrenWithUpdate).toBe(0)

  // And the activity bar is where a person finds it: the bottom group carries
  // the update entry (absent until a release is available, so the control is
  // asserted by its seam rather than by its presence). The seam was waited for
  // above, so this read is of a mounted bar rather than of the shell.
  const bottomZone = await page.evaluate(
    () => document.querySelectorAll('.activity-bar-bottom').length,
  )
  expect(bottomZone).toBe(1)
})

test('the tab strip lead is the first control, with no update or condition child', async ({
  page,
}) => {
  await page.goto('/')
  await expect(page.locator(BAR)).toBeVisible()
  await expect(page.locator(LEAD)).toBeVisible()

  const layout = await page.evaluate(() => {
    const bar = document.querySelector('.tabbar')
    if (!(bar instanceof HTMLElement)) throw new Error('tabbar is not mounted')
    const children = [...bar.children]
    const roles = children.map((child) => {
      if (child.querySelector('.update-notice') !== null) return 'update'
      if (child.matches('.tabstrip-lead')) return 'lead'
      if (child.matches('.tabs-container')) return 'tabs'
      if (child.matches('.tabstrip-actions')) return 'actions'
      if (child.matches('.tabbar-spacer')) return 'spacer'
      if (child.matches('.ui-connection-overlay, .ui-connection-indicator, .connection-notice')) {
        return 'connection'
      }
      return 'other'
    })
    return { roles, first: roles[0] ?? '', lead: children[0]?.matches('.tabstrip-lead') ?? false }
  })

  // Exactly the strip's own parts: no update child (the offer moved out on
  // nocx-1ebrs.66.1.5) and no condition child (connection state owns a
  // top-layer overlay instead of a strip child).
  expect(layout.roles).toEqual(['lead', 'tabs', 'actions', 'spacer'])
  expect(layout.roles).not.toContain('update')
  expect(layout.roles).not.toContain('connection')
  expect(layout.roles).not.toContain('other')
  expect(layout.lead).toBe(true)
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
