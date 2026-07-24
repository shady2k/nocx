import { test, expect } from '@playwright/test'

// nocx-4ff command-editor regressions. These run against the real app under
// `wails dev` (the repo's webServer). On a headless box with no WebKitGTK the
// same specs run against a vite + devharness shim: set NOCX_WS_PORT to the
// devharness WS port and baseURL to the vite origin, and the beforeEach below
// injects the Wails GetWSPort binding the frontend expects. Under `wails dev`
// the real binding is present and the shim is a no-op.
test.beforeEach(async ({ page }) => {
  const port = process.env.NOCX_WS_PORT
  if (!port) return
  await page.addInitScript((p) => {
    // Minimal stand-in for the Wails runtime bindings main.ts calls.
    ;(window as unknown as { go: unknown }).go = {
      main: {
        WailsApp: {
          GetWSPort: () => Promise.resolve(Number(p)),
          CheckForUpdate: () => Promise.resolve(null),
          ReportHealthy: () => Promise.resolve(),
          ApplyUpdate: () => Promise.resolve(),
        },
      },
    }
  }, port)
})

const TITLE = '.tab-title'
const EDITOR = '.nocx-editor'
const INPUT = '.nocx-editor-input'

async function waitForPrompt(page: import('@playwright/test').Page) {
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', {
    timeout: 15000,
  })
}

test.describe('command editor (nocx-4ff)', () => {
  // A clean local prompt owns input immediately — the editor must not wait for a
  // command to run first. Regression for the spurious OSC 133 C emitted while
  // nocx.bash was being sourced, which left the first prompt untrusted.
  test('editor is visible at the first prompt', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
  })

  // The editor sits at z-index:20 above every xterm layer. Regression for the
  // WebGL link-layer canvas (z-index:2) that won hit-testing over the editor,
  // so every click, caret move and word-select landed on the terminal canvas.
  test('mouse hit-tests the textarea, not the terminal canvas', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
    await page.locator(INPUT).fill('echo hello world foobar')

    const hitTag = await page.evaluate(() => {
      const el = document.querySelector('.nocx-editor-input') as HTMLElement
      const r = el.getBoundingClientRect()
      return document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)?.tagName ?? null
    })
    expect(hitTag).toBe('TEXTAREA')
  })

  test('double-click selects a word in the editor', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
    await page.locator(INPUT).fill('echo hello world foobar')

    const box = (await page.locator(INPUT).boundingBox())!
    await page.mouse.dblclick(box.x + 120, box.y + box.height / 2)

    const selLen = await page.evaluate(() => {
      const t = document.querySelector('.nocx-editor-input') as HTMLTextAreaElement
      return t.selectionEnd - t.selectionStart
    })
    expect(selLen).toBeGreaterThan(0)
  })

  test('the submit button is clickable and submits', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })
    await page.locator(INPUT).fill('echo clickme')

    await page.locator('.nocx-editor-submit').click()
    // Submit clears the composed line (atomic handoff) — proof the click landed.
    await expect(page.locator(INPUT)).toHaveValue('', { timeout: 3000 })
  })

  // One submission is one block. A multi-line composition is a single command
  // the user entered once, not one block per line (item 3).
  test('a multi-line command is one gutter landmark, not three', async ({ page }) => {
    await waitForPrompt(page)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 8000 })

    const glyphs = () => page.locator('.nocx-gutter-glyph').count()
    const before = await glyphs()

    await page.locator(INPUT).fill('echo one\necho two\necho three')
    await page.keyboard.press('Enter')

    await expect.poll(async () => (await glyphs()) - before, { timeout: 5000 }).toBe(1)
  })
})
