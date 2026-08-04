import { test, expect, promptReady } from './harness'

/**
 * nocx-4ff.29 — returning to a tab must hand input back to whatever owns it.
 *
 * `multi-tab-input.spec.ts` covers the neighbouring defect (a tab that is active
 * accepts keystrokes) but deliberately clicks into the editor first, with a
 * comment saying auto-focus after a switch is a separate bug. That click is what
 * kept this case unwatched: the user does not click, they press a key — and the
 * report is that the returned-to tab reacts to neither.
 *
 * Both halves are asserted, because they fail differently:
 *   1. focus lands in the editor of the tab that just became active, and
 *   2. a keystroke actually reaches that tab's shell.
 * Asserting only (2) would pass on a build where focus is wrong but some bounce
 * handler rescues the first keypress; asserting only (1) would pass on a build
 * where the editor is focused and read-only.
 */

const TITLE = '.nocx-tab-title'
const TAB_ADD = '[aria-label="New tab"]'

/** The class of the focused element, scoped to nothing — focus is global. */
const focusedClass = (page: import('@playwright/test').Page) =>
  page.evaluate(() => document.activeElement?.className ?? '')

test.describe('focus after switching back to a tab (nocx-4ff.29)', () => {
  test('the returned-to tab owns the keyboard without a click', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await promptReady(page)

    await page.locator(TAB_ADD).click()
    await expect(page.getByRole('tab')).toHaveCount(2)
    await promptReady(page)
    const tab2Title = await page.locator(TITLE).nth(1).textContent()

    // Back to tab 1 — by clicking its TAB, which is not the same as clicking
    // into its content. Nothing else touches the pane from here on.
    await page.getByRole('tab').first().click()
    await expect(page.getByRole('tab').first()).toHaveAttribute('aria-selected', 'true')

    // (1) Focus is in an editor input, not in the read-only grid's textarea.
    await expect.poll(() => focusedClass(page), { timeout: 5000 }).toContain('nocx-editor-input')

    // And specifically in THIS tab's editor: every open tab has one, so a focus
    // that stayed in tab 2's editor satisfies the check above while being the
    // very bug — the keystroke would go to the other shell.
    const focusedInActivePane = await page.evaluate(() => {
      const active = document.querySelector('.pane.active')
      const el = document.activeElement
      return active !== null && el !== null && active.contains(el)
    })
    expect(focusedInActivePane).toBe(true)

    // (2) A keystroke reaches tab 1's shell. OSC 0 is the cheapest observable
    // round trip: it proves the bytes reached the PTY and came back.
    const marker = `SW-${Date.now().toString(36)}`
    await page.keyboard.type(`printf '\\033]0;${marker}\\007'`)
    await page.keyboard.press('Enter')

    await expect(page.locator(TITLE).first()).toHaveText(marker, { timeout: 5000 })
    // The other tab must not have received it.
    await expect(page.locator(TITLE).nth(1)).toHaveText(tab2Title!, { timeout: 2000 })
  })

  /**
   * The editor of a tab you switched away from must not be painted.
   *
   * Every tab owns its editor — that is what keeps a half-typed command across a
   * switch — and every pane lays them out at the same place, so a second one that
   * still paints lands exactly on top of the active tab's. The typed text goes to
   * the right place and the user watches the wrong, empty box: keystrokes reach
   * the PTY, so the assertions above stay green through the whole defect.
   *
   * Measured as computed visibility rather than by class or inline style. An
   * inactive pane is hidden with `visibility: hidden`, which a descendant can
   * override — reading the pane's class would report the intent, not the paint.
   */
  test('only the active tab paints an editor', async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
    await page.locator(TAB_ADD).click()
    await expect(page.getByRole('tab')).toHaveCount(2)
    await promptReady(page)
    await page.getByRole('tab').first().click()
    await expect(page.getByRole('tab').first()).toHaveAttribute('aria-selected', 'true')

    const painted = await page.evaluate(() =>
      [...document.querySelectorAll('.nocx-editor')]
        .filter((el) => getComputedStyle(el).visibility === 'visible')
        .map((el) => el.closest('.pane')?.className ?? '(no pane)'),
    )
    expect(painted).toEqual(['pane active'])
  })

  test('clicking into the returned-to tab also restores input', async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
    await page.locator(TAB_ADD).click()
    await expect(page.getByRole('tab')).toHaveCount(2)
    await promptReady(page)

    await page.getByRole('tab').first().click()

    // The click the user reported as not helping either. Aimed near the bottom
    // of the pane, where the prompt editor sits.
    const box = await page.locator('.pane.active').boundingBox()
    await page.mouse.click(box!.x + box!.width / 2, box!.y + box!.height - 30)

    await expect.poll(() => focusedClass(page), { timeout: 5000 }).toContain('nocx-editor-input')

    const marker = `CL-${Date.now().toString(36)}`
    await page.keyboard.type(`printf '\\033]0;${marker}\\007'`)
    await page.keyboard.press('Enter')
    await expect(page.locator(TITLE).first()).toHaveText(marker, { timeout: 5000 })
  })
})
