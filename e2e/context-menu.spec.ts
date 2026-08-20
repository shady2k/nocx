import { test, expect } from './harness'

const MORE = '[aria-label="More"]'
const MENU_ITEM = '.ui-context-menu__item'
const MENU_ICON = '.ui-context-menu__icon'
const TAB = '.nocx-tab'

/**
 * e2e: THE MARK ON A MENU ROW IS VISIBLE (nocx-8830c).
 *
 * A glyph is the fastest way back to an action a person has used before —
 * they stop reading the menu and start pointing at it — which is what
 * `ContextMenuItem.icon` exists for. In the packaged macOS app there was no
 * glyph on any row: the labels sat indented past an icon column that was
 * reserved and empty, in every menu, on both of the strip's menus and on the
 * file tree's.
 *
 * The kit's icons are inline `<svg viewBox>` components with no width or
 * height of their own — they take their size from the slot they are dropped
 * into, and every slot in the kit states it (`.ui-search-field__icon svg`,
 * `.ui-toast__icon svg`, and nine more). `.ui-context-menu__icon` was the one
 * that did not, so the svg fell back to its intrinsic size: 14×14 in Chromium
 * and 0×0 in WebKit, which is what the packaged app runs.
 *
 * WHICH IS WHY THE SIZE IS ASSERTED AND NOT THE PRESENCE. The element was in
 * the document the whole time and every jsdom test that asked for it found
 * it; only its box was empty, and only in one engine. This case fails on the
 * `webkit` project and passes on `chromium` either way.
 */
test.describe('context menu (kit)', () => {
  /** Every mark in the open menu, as a rendered box. */
  async function markBoxes(page: import('./harness').Page) {
    return page.$$eval(MENU_ICON, (slots) =>
      slots.map((slot) => {
        const svg = slot.querySelector('svg')
        const box = svg?.getBoundingClientRect()
        return {
          hasSvg: svg !== null,
          width: box?.width ?? 0,
          height: box?.height ?? 0,
        }
      }),
    )
  }

  test("the strip's menu paints the mark on every row that has one", async ({ page }) => {
    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)

    await page.locator(MORE).first().click()
    await expect(page.locator(MENU_ITEM).first()).toBeVisible({ timeout: 10_000 })

    const marks = await markBoxes(page)
    // Every row of this menu carries an icon (tab-strip.tsx): quick connect,
    // the secret picker, snippets, a new workspace.
    expect(marks.length).toBeGreaterThan(0)
    for (const mark of marks) {
      expect(mark.hasSvg).toBe(true)
      expect(mark.width).toBeGreaterThan(0)
      expect(mark.height).toBeGreaterThan(0)
    }
  })

  test("a tab's own menu paints its marks too", async ({ page }) => {
    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)

    await page.locator(TAB).first().click({ button: 'right' })
    await expect(page.locator(MENU_ITEM).first()).toBeVisible({ timeout: 10_000 })

    const marks = await markBoxes(page)
    expect(marks.length).toBeGreaterThan(0)
    for (const mark of marks) {
      expect(mark.hasSvg).toBe(true)
      expect(mark.width).toBeGreaterThan(0)
      expect(mark.height).toBeGreaterThan(0)
    }
  })
})
