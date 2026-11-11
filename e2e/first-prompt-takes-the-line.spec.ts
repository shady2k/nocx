import { test, expect, promptReady } from './harness'

/** e2e: a fresh tab's first prompt takes the line (nocx-evj4b.1.14).
 *
 * The owner's rule (2026-11-10): until the session is ready to take a command,
 * nocx does not take it. The person's shape is the whole spec — open a new tab,
 * and as soon as its prompt shows, type a command and press Enter. The command
 * runs and its output is in the pane.
 *
 * Nothing here waits for a probe, a "second prompt" or any duration: the only
 * barrier is the product's own prompt (the focused composer of the active
 * pane), which is the exact moment a person starts typing.
 */

const TAB = '.nocx-tab'
const nonce = Date.now().toString(36)
const MARKER = `first-prompt-line-${nonce}`

test('a fresh tab runs the first command typed at its prompt', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)
  await promptReady(page)

  await page.keyboard.press('Meta+t')
  await expect(page.locator(TAB)).toHaveCount(2)
  // THE PROMPT SHOWS: the new tab's composer owns the keyboard. This is the
  // earliest instant a person can type, and it is a product state, not a delay.
  await promptReady(page)

  await page.keyboard.type(`printf '${MARKER}\\n'`)
  await page.keyboard.press('Enter')

  // "The command ran", observable at the product's own surface: a settled block
  // in the active pane carries its output.
  await expect(page.locator('.pane.active .cmd-block:not(.cmd-block-running)')).toContainText(
    MARKER,
    { timeout: 20_000 },
  )
})
