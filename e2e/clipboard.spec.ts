import { test, expect, promptReady } from './harness'

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
const INPUT = '.nocx-editor-input'

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
    await expect(page.getByRole('tab')).toHaveCount(1)

    await promptReady(page)

    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

    const marker = `CT-${Date.now().toString(36)}`

    // Echo a unique marker.  Post-scrollback the xterm viewport is cleared
    // after each command (OSC D → clearViewport), so the echoed text lives
    // in a scrollback DOM block — not in the xterm canvas.  We select from
    // the scrollback block and the scrollback mouseup handler copies to the
    // clipboard via the same BrowserClipboard path.
    await page.keyboard.type(`printf '\\033]0;${marker}\\007' && echo ${marker}`)
    await page.keyboard.press('Enter')

    // Find the scrollback block containing the marker.
    const block = page.locator('.cmd-block', { hasText: marker }).first()
    await expect(block).toBeVisible({ timeout: 5000 })

    // Let Playwright wait for the block's position to settle before selecting;
    // measuring coordinates first can race the next prompt block being added.
    await block.click({ clickCount: 3 })
    await expect
      .poll(() => page.evaluate(() => window.getSelection()?.toString() ?? ''), { timeout: 3000 })
      .toContain(marker)

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
    await expect(page.getByRole('tab')).toHaveCount(1)

    await promptReady(page)

    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

    // The expected output is not present verbatim in the pasted command, so
    // finding it in a completed block proves that Enter executed the paste.
    const suffix = Date.now().toString(36)
    const pasteMarker = `PT-${suffix}`
    const pastedCommand = `printf 'PT-%s\\n' '${suffix}'`
    await page.evaluate(async (command) => {
      await navigator.clipboard.writeText(command)
    }, pastedCommand)

    // Right-click near the bottom of the pane where the editor lives.
    // The contextmenu handler on the pane pastes to the editor when it is
    // visible; clicking the xterm area may have its own handler.
    const box = await page.locator(PANE).boundingBox()
    if (!box) throw new Error('pane not found')
    await page.mouse.click(box.x + box.width / 2, box.y + box.height - 30, {
      button: 'right',
    })

    // Wait for the paste to land in the editor. The input surface is CM6's
    // contenteditable contentDOM (ADR-0010), so the text is read back from the
    // DOM, not from a .value property.
    await expect(page.locator(INPUT)).toHaveText(pastedCommand, {
      timeout: 3000,
    })

    // Execute the pasted command and wait for its completed output block.
    await page.keyboard.press('Enter')
    await expect(page.locator('.cmd-block', { hasText: pasteMarker }).first()).toBeVisible({
      timeout: 5000,
    })
  })
})
