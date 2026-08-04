import { test, expect, promptReady } from './harness'

// nocx-4ff.28: opening a second tab leaves the first unable to accept
// keyboard input. The user-observable contract is that every tab accepts
// keystrokes when it is active, regardless of how many other tabs exist.

const TITLE = '.nocx-tab-title'
const TAB_ADD = '[aria-label="New tab"]'
const PANE = '.pane.active'

test.describe('multi-tab input (nocx-4ff.28)', () => {
  test('first tab still accepts input after second tab is created', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await promptReady(page)

    // Record tab 1's current title so we can assert it changes.
    const tab1InitialTitle = await page.locator(TITLE).first().textContent()

    // Create a second tab.
    await page.locator(TAB_ADD).click()
    await expect(page.getByRole('tab')).toHaveCount(2)

    // Wait for tab 2's prompt to be ready.
    await promptReady(page)

    // Record tab 2's initial title.
    const tab2InitialTitle = await page.locator(TITLE).nth(1).textContent()

    // Switch back to tab 1 by clicking its tab button.
    await page.getByRole('tab').first().click()

    // Click into the editor area of tab 1 to give it focus.
    // Post tab-switch the editor may not auto-focus (nocx-4ff.29); this
    // test must isolate nocx-4ff.28 (typing in an active tab with focus
    // in its editor), so we manually place focus first.
    const box = await page.locator(PANE).boundingBox()
    await page.mouse.click(box!.x + box!.width / 2, box!.y + box!.height - 30)
    await expect
      .poll(() => page.evaluate(() => document.activeElement?.className ?? ''), { timeout: 5000 })
      .toContain('nocx-editor-input')

    // Now type a command that sets the OSC 0 title.
    // If Tab 2's _globalKeydown steals focus, the keystroke lands in tab 2's
    // editor and tab 1's title never changes.
    const marker = `T1-${Date.now().toString(36)}`
    await page.keyboard.type(`printf '\\033]0;${marker}\\007'`)
    await page.keyboard.press('Enter')

    // Tab 1's title must reflect the keystroke.
    await expect(page.locator(TITLE).first()).toHaveText(marker, { timeout: 5000 })

    // Tab 2's title must NOT have changed.
    await expect(page.locator(TITLE).nth(1)).toHaveText(tab2InitialTitle!, { timeout: 2000 })

    // And tab 1's title is definitely different from initial.
    //
    // This was missing its await, which is why nocx-ifgp looked load-dependent:
    // an unawaited web-first assertion returns a promise nobody holds, so it
    // never runs at the point it is written and its eventual rejection surfaces
    // somewhere else — or not at all. It asserted nothing on a good day and
    // failed the run on a bad one.
    await expect(page.locator(TITLE).first()).not.toHaveText(tab1InitialTitle!, {
      timeout: 5000,
    })
  })

  test('second tab accepts input while it is active', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await promptReady(page)

    // Create a second tab.
    await page.locator(TAB_ADD).click()
    await expect(page.getByRole('tab')).toHaveCount(2)
    await promptReady(page)

    // Tab 2 is now active. Type a command into it.
    const marker = `T2-${Date.now().toString(36)}`
    await page.keyboard.type(`printf '\\033]0;${marker}\\007'`)
    await page.keyboard.press('Enter')

    // Tab 2's title must reflect the keystroke (nth(1) = second tab).
    await expect(page.locator(TITLE).nth(1)).toHaveText(marker, { timeout: 5000 })
  })
})
