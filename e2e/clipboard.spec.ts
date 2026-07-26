import { test, expect } from './harness'

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
        void _value /* swallowed */
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

    await expect(page.locator(TITLE).first()).not.toHaveText('', {
      timeout: 10000,
    })

    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

    const marker = `CT-${Date.now().toString(36)}`

    // Echo a unique marker.  Post-scrollback the xterm viewport is cleared
    // after each command (OSC D → clearViewport), so the echoed text lives
    // in a scrollback DOM block — not in the xterm canvas.  We select from
    // the scrollback block and the scrollback mouseup handler copies to the
    // clipboard via the same BrowserClipboard path.
    await page.keyboard.type(`printf '\\033]0;${marker}\\007' && echo ${marker}`)
    await page.keyboard.press('Enter')
    await expect(page.locator(TITLE).first()).toHaveText(marker, {
      timeout: 5000,
    })

    // Find the scrollback block containing the marker.
    const block = page.locator('.cmd-block', { hasText: marker }).first()
    await expect(block).toBeVisible({ timeout: 3000 })

    // Select the text inside the block via triple-click, which the
    // scrollback mouseup handler copies to the clipboard.
    const box = await block.boundingBox()
    if (!box) throw new Error('cmd-block not found')
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { clickCount: 3 })

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
      await navigator.clipboard.writeText(`printf '\\033]0;${marker}\\007'`)
    }, pasteMarker)

    // Right-click near the bottom of the pane where the editor lives.
    // The contextmenu handler on the pane pastes to the editor when it is
    // visible; clicking the xterm area may have its own handler.
    const box = await page.locator(PANE).boundingBox()
    if (!box) throw new Error('pane not found')
    await page.mouse.click(box.x + box.width / 2, box.y + box.height - 30, {
      button: 'right',
    })

    // Wait for the paste to land in the editor.
    await expect(page.locator('.nocx-editor-input')).toHaveValue(new RegExp(pasteMarker), {
      timeout: 3000,
    })

    // Execute the pasted command. If paste worked, the title changes.
    await page.keyboard.press('Enter')
    await expect(page.locator(TITLE).first()).toHaveText(pasteMarker, {
      timeout: 3000,
    })
  })
})
