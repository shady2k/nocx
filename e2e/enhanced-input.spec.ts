import { test, expect } from '@playwright/test'

// nocx-4ff.4: verify that raw input routing works after an enhanced-input
// submit — the editor must stay hidden while a program runs, and typed keys
// must reach the PTY rather than the editor.

const PANE = '.pane.active'
const TITLE = '.tab-title'

test.describe('enhanced input raw routing', () => {
  test('read command receives input after enhanced submit', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    // Wait for the shell to be ready (title populated).
    await expect(page.locator(TITLE).first()).not.toHaveText('', {
      timeout: 10000,
    })

    // Type a read command at the prompt and submit it.
    // read reads a line from stdin into variable x, then echo prints it.
    await page.keyboard.type('read x; echo "got:$x"\n')

    // The `read` builtin is waiting for stdin. Type the answer.
    await page.keyboard.type('hello')

    // Submit the answer — it should reach read, not the editor.
    await page.keyboard.press('Enter')

    // The shell should now echo "got:hello" — proof the input reached
    // the running program, not the editor.
    const pane = page.locator(PANE)
    await expect(pane).toContainText('got:hello', { timeout: 5000 })
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

    // Type a complete command; it should work after Ctrl-C.
    const marker = `RW-${Date.now().toString(36)}`
    await page.keyboard.type(
      `printf '\\033]0;${marker}\\007' && echo ${marker}\n`,
    )
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

    const marker = `MS-${Date.now().toString(36)}`

    // Run several commands in rapid succession — each submit must leave
    // the state machine in RUNNING_RAW (owned:false) so the next command
    // prompt returns via markers, not stale editor state.
    for (let i = 0; i < 3; i++) {
      await page.keyboard.type(`echo ${marker}-${i}\n`)
    }

    // All three echoes should appear in the terminal output.
    const pane = page.locator(PANE)
    await expect(pane).toContainText(`${marker}-0`, { timeout: 5000 })
    await expect(pane).toContainText(`${marker}-1`, { timeout: 1000 })
    await expect(pane).toContainText(`${marker}-2`, { timeout: 1000 })
  })
})
