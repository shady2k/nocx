import { test, expect, promptReady, clickIntoEditor } from './harness'

// Terminal clipboard e2e: copy-on-select, and the right-click that is no longer a paste.
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

// RUNS ON BOTH BROWSERS, unlike everything below it. The Chromium-only note
// above is about reading and writing the clipboard, which needs permissions
// WebKit does not grant under Playwright. This test never touches a clipboard:
// the banner is what a BLOCKED OSC 52 write raises, so the assertion is about
// the panes surviving the answer. Do not "fix" a WebKit failure here by adding
// a skip — a failure here means the panes are gone, on the browser closest to
// the shipped WKWebView.
test('answering the OSC 52 banner preserves every open terminal pane', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('.nocx-tab')).toHaveCount(1)
  await promptReady(page)

  await page.locator('[aria-label="New tab"]').click()
  await expect(page.locator('.nocx-tab')).toHaveCount(2)
  await promptReady(page)

  await clickIntoEditor(page)
  await page.keyboard.type(`printf '\\033]52;c;Tk9DWA==\\007'`)
  await page.keyboard.press('Enter')

  const banner = page.locator('.clipboard-banner')
  await expect(banner).toBeVisible()
  await banner.getByRole('button', { name: "Don't show again" }).click()
  await expect(banner).toHaveCount(0)
  await expect(page.locator('.pane')).toHaveCount(2)

  const tabs = page.locator('.nocx-tab')
  const markers = [`OSC52-A-${Date.now().toString(36)}`, `OSC52-B-${Date.now().toString(36)}`]

  for (const [index, marker] of markers.entries()) {
    await tabs.nth(index).click()
    await promptReady(page)
    await clickIntoEditor(page)
    // EACH TAB'S OWN SHELL ANSWERS, and the answer is its command's block.
    // The instrument used to be the tab's title (`printf '\033]0;…'`), which
    // stopped being an echo of what the shell received when nocx-ouru5.1.1
    // made a program's title end with that program's run. A block on screen
    // exists only because the bytes reached THIS tab's shell.
    await page.keyboard.type(`echo ${marker}`)
    await page.keyboard.press('Enter')
    await expect(page.locator('.pane.active .cmd-block', { hasText: marker })).toBeVisible({
      timeout: 10_000,
    })
  }
})

// ── copy-on-select ──────────────────────────────────────────────────────

test.describe('copy-on-select', () => {
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'clipboard-read permission is Chromium-only; WebKit must be checked manually',
  )

  test('selecting terminal text copies it to the clipboard', async ({ page }) => {
    await disableWailsRuntime(page)
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

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

    // THE MARKER IN THE COMMAND LINE IS NOT THE OUTPUT. A `.cmd-block`
    // filtered by text is satisfied by the RUNNING block, whose centre is the
    // live grid (whose triple click belongs to the painter) or the
    // non-selectable header; and the freeze replaces the element between the
    // three presses, taking the browser's range with it (measured: 1 run in
    // 40 left window.getSelection() empty although the marker was on screen).
    // `.cmd-output` exists only once the block has frozen, and the prompt says
    // the composer is back, so neither the click nor its selection races the
    // end of the command.
    const output = page.locator('.cmd-block .cmd-output', { hasText: marker }).first()
    await expect(output).toBeVisible({ timeout: 5000 })
    await promptReady(page)
    await output.scrollIntoViewIfNeeded()
    await output.click({ clickCount: 3 })
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

  // A person selects with a drag, not a triple click (nocx-zg3k3.15.3). The
  // press focuses the block, and the pane's focus bounce sent the focus
  // straight back to the editor, whose CodeMirror then took the document
  // selection back: no drag over block output selected anything, while the
  // triple-click case above, which sets its range itself, stayed green.
  test('dragging over a finished block selects its text, copies it, and the next key types', async ({
    page,
  }) => {
    await disableWailsRuntime(page)
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await promptReady(page)
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

    const marker = `DRAG-${Date.now().toString(36)}`
    await page.keyboard.type(`echo ${marker}-tail`)
    await page.keyboard.press('Enter')
    const output = page.locator('.cmd-block .cmd-output', { hasText: marker }).first()
    await expect(output).toBeVisible({ timeout: 5000 })
    await promptReady(page)
    // A second command moves the marker's block up, clear of the composer.
    await page.keyboard.type('echo spacer')
    await page.keyboard.press('Enter')
    await expect(
      page.locator('.cmd-block .cmd-output', { hasText: 'spacer' }).first(),
    ).toBeVisible()
    await promptReady(page)
    await output.scrollIntoViewIfNeeded()

    // The drag starts on the marker's first character and ends past its last,
    // the way a person drags over a word they can see.
    const text = await output.evaluate((el, m) => {
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const at = n.textContent?.indexOf(m) ?? -1
        if (at < 0) continue
        const range = document.createRange()
        range.setStart(n, at)
        range.setEnd(n, at + m.length + '-tail'.length)
        const r = range.getBoundingClientRect()
        return { x: r.left, y: r.top + r.height / 2, width: r.width }
      }
      return null
    }, marker)
    if (!text) throw new Error('the marker is not in the block output')
    await page.mouse.move(text.x + 1, text.y)
    await page.mouse.down()
    await page.mouse.move(text.x + text.width / 2, text.y, { steps: 5 })
    await page.mouse.move(text.x + text.width + 3, text.y, { steps: 5 })
    await page.mouse.up()

    await expect
      .poll(() => page.evaluate(() => window.getSelection()?.toString() ?? ''), { timeout: 3000 })
      .toContain(`${marker}-tail`)
    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()), { timeout: 3000 })
      .toContain(`${marker}-tail`)

    // The selection stays on screen, and the next key still types at the
    // prompt.
    await page.keyboard.type('x')
    await expect(page.locator('.pane.active .nocx-editor .cm-content')).toContainText('x')
  })

  // A double click on a word in the live terminal selects that word and copies
  // it, by the frozen block's own word rule (nocx-zg3k3.15.4).
  test('double-clicking a word in a running program copies the word', async ({ page }) => {
    await disableWailsRuntime(page)
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await promptReady(page)
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    const word = `~/dbl-${Date.now().toString(36)}.txt`
    await page.keyboard.type(`printf 'before ${word} after'; sleep 30`)
    await page.keyboard.press('Enter')
    // The command's own echo holds the word too; the output row is the one
    // without the printf.
    const row = page
      .locator('.pane.active .terminal-live-grid .term-grid-row', { hasText: word })
      .filter({ hasNotText: 'printf' })
    await expect(row).toHaveCount(1, { timeout: 15_000 })
    const at = await row.evaluate((el, w) => {
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const i = n.textContent?.indexOf(w) ?? -1
        if (i < 0) continue
        const r = document.createRange()
        r.setStart(n, i + 3)
        r.setEnd(n, i + 4)
        const box = r.getBoundingClientRect()
        const rowBox = el.getBoundingClientRect()
        return {
          x: box.left - rowBox.left + box.width / 2,
          y: box.top - rowBox.top + box.height / 2,
        }
      }
      return null
    }, word)
    if (!at) throw new Error('the word is not on the painted row')
    await row.dblclick({ position: at })
    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()), { timeout: 3000 })
      .toBe(word)
    await page.keyboard.press('Control+C')
  })
})

// ── paste ───────────────────────────────────────────────────────────────

test.describe('paste', () => {
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'clipboard-read + clipboard-write require Chromium',
  )

  test('right-click no longer pastes — the chord keeps that job', async ({ page }) => {
    // nocx-a9x6u, the owner's 2026-11-08 pass: right-click-in-the-terminal
    // pasted the clipboard at the cursor, so a person aiming at a block's
    // actions inserted their clipboard into a command. The gesture is gone,
    // paste stays on Cmd/Ctrl+V — whose own path (the editor takes the focus
    // and the browser's native paste) is terminal-content.test.ts's "Cmd/Ctrl+V
    // redirects to the editor". A block's right-click now opens its ⋮ menu;
    // e2e/block-right-click.spec.ts carries that half.
    await disableWailsRuntime(page)
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    await promptReady(page)

    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

    const suffix = Date.now().toString(36)
    const clipboardText = `printf 'PT-%s\\n' '${suffix}'`
    await page.evaluate(async (command) => {
      await navigator.clipboard.writeText(command)
    }, clipboardText)

    await clickIntoEditor(page, { button: 'right' })

    // The input surface is CM6's contenteditable contentDOM (ADR-0010), so the
    // text is read back from the DOM, not from a .value property.
    await expect(page.locator(INPUT)).not.toContainText(clipboardText)
    // And nothing ran: a paste into the editor that then lost its way into the
    // PTY would show as a completed sprintf block.
    await expect(page.locator('.cmd-block', { hasText: `PT-${suffix}` })).toHaveCount(0)
  })
})
