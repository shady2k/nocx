import { test, expect, promptReady } from './harness'

// nocx-4ff.4: verify that raw input routing works after an enhanced-input
// submit — the editor must stay hidden while a program runs, and typed keys
// must reach the PTY rather than the editor.

const INPUT = '.nocx-editor-input'

test.describe('enhanced input raw routing', () => {
  test('read command receives input after enhanced submit', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)

    await promptReady(page)

    // Read a line into x, then print got-<x>. Completed command output is
    // frozen into a DOM scrollback block even though live xterm output is a
    // WebGL canvas.
    await page.keyboard.type('read x; printf \'got-%s\\n\' "$x"')
    await page.keyboard.press('Enter')
    await expect(page.locator(INPUT)).not.toBeVisible({ timeout: 5000 })

    // The `read` builtin is now waiting for stdin. Typing must reach the running
    // program (RUNNING_RAW → editor hidden), not the editor.
    await page.keyboard.type('hello')
    await page.keyboard.press('Enter')

    // The completed block proves the input reached `read`, not the editor.
    await expect(page.locator('.cmd-block', { hasText: 'got-hello' }).first()).toBeVisible({
      timeout: 5000,
    })
  })

  test('Ctrl-C at a prompt does not trap input', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)

    await promptReady(page)

    // Type partial input then Ctrl-C to cancel.
    await page.keyboard.type('echo partial')
    await page.locator(INPUT).evaluate((input) => {
      input.addEventListener(
        'blur',
        () => {
          input.setAttribute('data-prompt-cycle', 'interrupted')
        },
        { once: true },
      )
    })
    await page.keyboard.press('Control+c')

    // The fresh prompt briefly hides (and blurs) the editor before showing and
    // focusing it again. Waiting for that cycle proves the PTY handled Ctrl-C;
    // an empty value alone would pass synchronously before the shell was ready.
    await expect(page.locator(INPUT)).toHaveAttribute('data-prompt-cycle', 'interrupted', {
      timeout: 5000,
    })
    await promptReady(page)
    await expect(page.locator(INPUT)).toHaveText('', {
      timeout: 5000,
    })

    // Type a complete command; it should work after Ctrl-C.
    const suffix = Date.now().toString(36)
    const marker = `RW-${suffix}`
    await page.keyboard.type(`printf 'RW-%s\\n' '${suffix}'`)
    await page.keyboard.press('Enter')
    await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
      timeout: 5000,
    })
  })

  test('multiple submits in succession all route raw', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)

    await promptReady(page)

    // Run several commands back-to-back — each submit must leave the state
    // machine in RUNNING_RAW (owned:false) so the next prompt returns via
    // markers, and each paste must NOT leak bracketed-paste wrappers into the
    // command. Each command prints a marker assembled by the shell so it does
    // not occur verbatim in the command text.
    for (let i = 0; i < 3; i++) {
      const marker = `MS-${i}`
      await page.keyboard.type(`printf 'MS-%s\\n' ${i}`)
      await page.keyboard.press('Enter')
      // Wait for this command's completed output before sending the next.
      // Without this gate the keystrokes for iteration i+1 can arrive
      // while the shell is still executing iteration i — the editor
      // input buffer and PTY stdin are not synchronised, and rapid
      // submission races.  A duration wait is not correct here either:
      // the only contract that matters is that each command has
      // finished when we send the next.  expect() polls, so this
      // converges as fast as the shell does.
      await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
        timeout: 5000,
      })
      await promptReady(page)
    }
  })
})
