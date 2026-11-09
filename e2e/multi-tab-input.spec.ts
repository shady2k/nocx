import { test, expect, promptReady, clickIntoEditor } from './harness'

// nocx-4ff.28: opening a second tab leaves the first unable to accept
// keyboard input. The user-observable contract is that every tab accepts
// keystrokes when it is active, regardless of how many other tabs exist.

const TAB = '.nocx-tab'
const TAB_ADD = '[aria-label="New tab"]'

/**
 * THE INSTRUMENT, and why it is not the tab's title any more.
 *
 * These cases used to type `printf '\033]0;MARK\007'` and watch the strip
 * take MARK: the cheapest observable round trip, because a program's title
 * came back as the tab's name. Since nocx-ouru5.1.1 a title a PROGRAM sets
 * belongs to that program's run and is gone when the command ends — so the
 * title stopped being an echo of what the shell received. A command whose
 * output the pane shows is the same round trip one layer lower: the block
 * exists only because the bytes reached this tab's shell.
 */
async function runInActiveTab(
  page: import('@playwright/test').Page,
  marker: string,
): Promise<void> {
  await page.keyboard.type(`echo ${marker}`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .cmd-block', { hasText: marker })).toBeVisible({
    timeout: 10_000,
  })
}

/** The tab that did NOT get the keystroke carries no trace of it. */
async function expectOtherTabsSilent(
  page: import('@playwright/test').Page,
  marker: string,
): Promise<void> {
  await expect(page.locator('.pane:not(.active) .cmd-block', { hasText: marker })).toHaveCount(0)
}

test.describe('multi-tab input (nocx-4ff.28)', () => {
  test('first tab still accepts input after second tab is created', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    await promptReady(page)

    // Create a second tab.
    await page.locator(TAB_ADD).click()
    await expect(page.locator(TAB)).toHaveCount(2)

    // Wait for tab 2's prompt to be ready.
    await promptReady(page)

    // Switch back to tab 1 by clicking its tab button.
    await page.locator(TAB).first().click()

    // Click into the editor of tab 1 to give it focus.
    // Post tab-switch the editor may not auto-focus (nocx-4ff.29); this
    // test must isolate nocx-4ff.28 (typing in an active tab with focus
    // in its editor), so we manually place focus first.
    await clickIntoEditor(page)
    await expect
      .poll(() => page.evaluate(() => document.activeElement?.className ?? ''), { timeout: 5000 })
      .toContain('nocx-editor-input')

    // Now type a command into tab 1. If tab 2's _globalKeydown steals focus,
    // the keystroke lands in tab 2's editor and tab 1's pane never shows it.
    const marker = `T1-${Date.now().toString(36)}`
    await runInActiveTab(page, marker)

    // Tab 2 must NOT have received the keystroke. Ordering is what makes this
    // check real rather than vacuous: tab 1's block is on screen already, so
    // the keystroke is known to have been routed and rendered by the time this
    // runs, and an empty answer here means tab 2's pane never got it.
    await expectOtherTabsSilent(page, marker)
  })

  test('second tab accepts input while it is active', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    await promptReady(page)

    // Create a second tab.
    await page.locator(TAB_ADD).click()
    await expect(page.locator(TAB)).toHaveCount(2)
    await promptReady(page)

    // Tab 2 is now active. Type a command into it — and it lands in the pane
    // in front, which is tab 2's.
    const marker = `T2-${Date.now().toString(36)}`
    await runInActiveTab(page, marker)
  })
})
