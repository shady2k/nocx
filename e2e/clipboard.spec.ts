import { test, expect } from '@playwright/test'

// Terminal clipboard e2e: copy-on-select, right-click paste.
//
// CLIPBOARD REALITY FOR PLAYWRIGHT:
// In `wails dev`, window.runtime is injected, so the app uses the Wails
// runtime clipboard (system clipboard). navigator.clipboard.{read,write}Text
// target the browser clipboard — a different data store. An assertion that
// writes via one and reads from the other is guaranteed to fail regardless
// of the implementation.
//
// Fix: disable the Wails runtime via addInitScript so the app falls back to
// BrowserClipboard (navigator.clipboard). Then grant clipboard permissions.
// This only works in Chromium — WebKit supports neither clipboard-read nor
// clipboard-write permissions in Playwright.
//
// Honest outcome: all tests are Chromium-only. WebKit must be checked by
// hand in a packaged build.

const PANE = '.pane.active'
const TITLE = '.tab-title'

async function disableWailsRuntime(page: import('@playwright/test').Page) {
  await page.addInitScript(() => {
    Object.defineProperty(window, 'runtime', {
      get() {
        return undefined
      },
      set(_value: unknown) {
        /* swallowed */
      },
      configurable: true,
      enumerable: true,
    })
  })
}

// ── copy-on-select ──────────────────────────────────────────────────────

test.describe('copy-on-select', () => {
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'clipboard-read permission is Chromium-only; WebKit must be checked manually',
  )

  test('selecting terminal text copies it to the clipboard', async ({ page }) => {
    await disableWailsRuntime(page)
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    // State-based shell-ready signal: the tab title is empty until the
    // session opens and the first renderer life-cycle completes.
    await expect(page.locator(TITLE).first()).not.toHaveText('', {
      timeout: 10000,
    })

    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

    // Clear the screen so the echoed marker lands on a predictable row
    // (row 1, just below the command line at row 0).
    await page.keyboard.type('clear\n')

    const marker = `CT-${Date.now().toString(36)}`

    // Type a command that both sets the terminal title and echoes the
    // marker. Waiting for the title to change is state-based: it proves
    // the shell consumed the input and the marker is on screen.
    await page.keyboard.type(
      `printf '\\033]0;${marker}\\007' && echo ${marker}\n`,
    )
    await expect(page.locator(TITLE).first()).toHaveText(marker, {
      timeout: 5000,
    })

    // Triple-click to select the echoed marker line. xterm.js line-select
    // on triple-click selects the entire row regardless of horizontal
    // position.
    //
    // After clear: row 0 = command line, row 1 = echo output.
    // Cell height = FONT_SIZE * LINE_HEIGHT = 14 * 1.2 = 16.8 px.
    // Row 0: padding-top (6) to 6+16.8 = 22.8 px from pane top.
    // Row 1: 22.8 to 39.6 px from pane top. 32 px = centre of row 1.
    const box = await page.locator(PANE).boundingBox()
    if (!box) throw new Error('pane not found')
    const y = box.y + 32
    await page.mouse.click(box.x + box.width / 2, y, { clickCount: 3 })

    // Poll the clipboard until the marker appears or the assertion times
    // out. The copy-on-select handler is async, so a single read may race.
    await expect
      .poll(
        async () => {
          return page.evaluate(() => navigator.clipboard.readText())
        },
        { timeout: 3000 },
      )
      .toContain(marker)
  })
})

// ── paste ───────────────────────────────────────────────────────────────

test.describe('paste', () => {
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'clipboard-read + clipboard-write require Chromium',
  )

  test('right-click pastes clipboard text at the cursor', async ({ page }) => {
    await disableWailsRuntime(page)
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    await expect(page.locator(TITLE).first()).not.toHaveText('', {
      timeout: 10000,
    })

    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

    // Put a command that sets the terminal title on the clipboard.
    const pasteMarker = `PT-${Date.now().toString(36)}`
    await page.evaluate(async (marker) => {
      await navigator.clipboard.writeText(
        `printf '\\033]0;${marker}\\007'`,
      )
    }, pasteMarker)

    // Right-click in empty space — not over a word. rightClickSelectsWord
    // is false, so even over a word there is no clipboard destruction, but
    // clicking empty space removes all doubt.
    const box = await page.locator(PANE).boundingBox()
    if (!box) throw new Error('pane not found')
    await page.mouse.click(
      box.x + box.width / 2,
      box.y + box.height / 2,
      { button: 'right' },
    )

    // Wait for the paste to land in the editor: readText() is async, and
    // at the prompt the terminal is read-only so text goes to the editor.
    // Pressing Enter before the paste resolves would submit the still-empty
    // editor (hiding it), then paste to the terminal with no CR — unexecuted.
    await expect(page.locator('.nocx-editor-input')).toHaveValue(
      new RegExp(pasteMarker),
      { timeout: 3000 },
    )

    // Execute the pasted command. If paste worked, the title changes.
    await page.keyboard.press('Enter')
    await expect(page.locator(TITLE).first()).toHaveText(pasteMarker, {
      timeout: 3000,
    })
  })
})
