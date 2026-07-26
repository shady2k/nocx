import { test, expect } from './harness'

// nocx-4ff.4: verify that raw input routing works after an enhanced-input
// submit — the editor must stay hidden while a program runs, and typed keys
// must reach the PTY rather than the editor.

const TITLE = '.tab-title'

test.describe('enhanced input raw routing', () => {
  test('read command receives input after enhanced submit', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    // Wait for the shell to be ready (title populated).
    await expect(page.locator(TITLE).first()).not.toHaveText('', {
      timeout: 10000,
    })

    // read a line into x, then set the terminal TITLE to got-<x>. Assert on the
    // title, not the pane: xterm renders text to a WebGL canvas, so terminal
    // output is never in the DOM — toContainText on the pane always sees "".
    await page.keyboard.type('read x; printf "\\033]0;got-$x\\007"')
    await page.keyboard.press('Enter')

    // The `read` builtin is now waiting for stdin. Typing must reach the running
    // program (RUNNING_RAW → editor hidden), not the editor.
    await page.keyboard.type('hello')
    await page.keyboard.press('Enter')

    // Title becomes got-hello ⇒ the input reached `read`, not the editor.
    await expect(page.locator(TITLE).first()).toHaveText('got-hello', {
      timeout: 5000,
    })
  })

  test('Ctrl-C at a prompt does not trap input', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    await expect(page.locator(TITLE).first()).not.toHaveText('', {
      timeout: 10000,
    })

    // Type partial input then Ctrl-C to cancel.
    await page.keyboard.type('echo partial')
    await page.keyboard.press('Control+c')

    // Wait for the editor to clear before typing the next command.
    // Ctrl-C cancels input and clears the editor textarea synchronously
    // in the DOM, but the PTY needs the signal to propagate before it
    // is ready for the next command.  Polling toHaveValue('') covers
    // both: the editor DOM is empty AND Playwright has observed the
    // settled state before issuing the next keystroke batch.
    await expect(page.locator('.nocx-editor-input')).toHaveValue('', {
      timeout: 5000,
    })

    // Type a complete command; it should work after Ctrl-C.
    const marker = `RW-${Date.now().toString(36)}`
    await page.keyboard.type(`printf '\\033]0;${marker}\\007' && echo ${marker}`)
    await page.keyboard.press('Enter')
    await expect(page.locator(TITLE).first()).toHaveText(marker, {
      timeout: 5000,
    })
  })

  test('multiple submits in succession all route raw', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    await expect(page.locator(TITLE).first()).not.toHaveText('', {
      timeout: 10000,
    })

    // Run several commands back-to-back — each submit must leave the state
    // machine in RUNNING_RAW (owned:false) so the next prompt returns via
    // markers, and each paste must NOT leak bracketed-paste wrappers into the
    // command. Each command sets the title to its own marker; the final title
    // proves the last of three rapid submits committed cleanly.
    for (let i = 0; i < 3; i++) {
      await page.keyboard.type(`printf "\\033]0;MS-${i}\\007"`)
      await page.keyboard.press('Enter')
      // Wait for this command's marker before sending the next.
      // Without this gate the keystrokes for iteration i+1 can arrive
      // while the shell is still executing iteration i — the editor
      // input buffer and PTY stdin are not synchronised, and rapid
      // submission races.  A duration wait is not correct here either:
      // the only contract that matters is that each command has
      // finished when we send the next.  expect() polls, so this
      // converges as fast as the shell does.
      await expect(page.locator(TITLE).first()).toHaveText(`MS-${i}`, {
        timeout: 5000,
      })
    }

    await expect(page.locator(TITLE).first()).toHaveText('MS-2', {
      timeout: 5000,
    })
  })
})
