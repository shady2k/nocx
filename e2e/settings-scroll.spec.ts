import { test, expect, type Page } from './harness'

// nocx-82l9.2: SettingsContent.mount appended an unclassed <div> into .pane, so
// flex:1 on .ui-page__scroll never received a bounded block size and the pane
// clipped its content instead of scrolling it. WebKit only — it is specifically
// sensitive to a missing min-height:0 in a flex chain.
//
// The probe used to be "scroll the last of a long list of setting rows into
// view". Settings no longer has a long list: it opens on one section at a time,
// and no section that contains rows overflows a short window (measured in WebKit
// scroller). The Backup & Restore section is the one that overflows at
// 1468px, so that is where the chain is now observable. The overflow assertion
// comes first on purpose: if that section ever shrinks, this test must fail
// loudly rather than pass because there was nothing to scroll (nocx-pp3y.1).

const BACKUP_SECTION = '.ui-grouped-nav__item[data-item="backup"]'

async function openOverflowingSection(page: Page): Promise<void> {
  await page.goto('/')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })
  await page.keyboard.press('Meta+,')
  await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })
  await page.locator(`${BACKUP_SECTION} button`).click()
}

/** The scroller must actually have something to scroll, or the rest proves nothing. */
async function expectOverflow(page: Page): Promise<void> {
  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const sc = document.querySelector<HTMLElement>('.ui-page__scroll')
          if (!sc) return 0
          return sc.scrollHeight - sc.clientHeight
        }),
      { timeout: 5000 },
    )
    .toBeGreaterThan(0)
}

/**
 * Scroll to the bottom and report whether the last section ends inside the PANE.
 *
 * The pane is the boundary that matters and the scroller is not: with the chain
 * unbound the scroller grows to content height, so "the last section fits inside
 * the scroller" is vacuously true precisely when the bug is present. Measured by
 * injecting `min-height: auto` over the chain in WebKit — the scroller-relative
 * comparison returned true, the pane-relative one returned false.
 */
async function lastSectionReachable(page: Page): Promise<boolean> {
  return page.evaluate(() => {
    const sc = document.querySelector<HTMLElement>('.ui-page__scroll')
    const pane = document.querySelector<HTMLElement>('.pane.active')
    if (!sc || !pane) return false
    sc.scrollTop = sc.scrollHeight
    const sections = sc.querySelectorAll<HTMLElement>('.ui-page-section')
    const last = sections[sections.length - 1]
    if (!last) return false
    // 1px of rounding slack — WebKit reports fractional heights here.
    return last.getBoundingClientRect().bottom <= pane.getBoundingClientRect().bottom + 1
  })
}

test.describe('settings scroll — normal', () => {
  test.use({ viewport: { width: 1024, height: 520 } })

  test('scrolls the last section into view in a short window (nocx-82l9.2)', async ({
    page,
    browserName,
  }) => {
    test.skip(browserName !== 'webkit', 'Settings scroll bug is WebKit-only (nocx-82l9.2)')

    await openOverflowingSection(page)
    await expectOverflow(page)

    expect(await lastSectionReachable(page)).toBe(true)
  })
})

test.describe('settings scroll — narrow', () => {
  test.use({ viewport: { width: 600, height: 520 } })

  test('scrolls the last section in narrow (stacked, <640px) layout', async ({
    page,
    browserName,
  }) => {
    test.skip(browserName !== 'webkit', 'Settings scroll bug is WebKit-only (nocx-82l9.2)')

    await openOverflowingSection(page)
    await expectOverflow(page)

    expect(await lastSectionReachable(page)).toBe(true)

    // The stacked rail trims its own chrome (base.css owns the narrow
    // breakpoint; the surface must not repaint the kit — rule 3). If the
    // compact padding ever moves back into a surface override, this fails.
    const railPad = await page.evaluate(() => {
      const rail = document.querySelector<HTMLElement>('.ui-page__rail')
      if (!rail) return null
      const cs = getComputedStyle(rail)
      return { top: cs.paddingTop, bottom: cs.paddingBottom }
    })
    expect(railPad).toEqual({ top: '8px', bottom: '8px' })
  })
})
