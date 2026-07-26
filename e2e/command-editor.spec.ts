import { test, expect } from './harness' // shared Wails WS-port shim for headless CI

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
})
