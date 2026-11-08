/**
 * e2e: one right-click, three surfaces it must not confuse (nocx-a9x6u).
 *
 * The owner, 2026-11-08: take the paste away from the right click, and open on
 * a block the same context menu its three dots open. The pane pasted its
 * clipboard on every right-click, on every surface — into the composer, or
 * into the program that was running — so a person aiming at a block's actions
 * got their clipboard inserted into a command instead.
 *
 * Three claims, one per test:
 *   1. a block's right-click opens the SAME menu its ⋮ opens, AT THE POINTER,
 *      and pastes nothing; a right-click on the live grid does nothing;
 *   2. a RUNNING block's menu is its own — Stop is on it — and the program is
 *      sent no paste;
 *   3. a full-screen program that asked for mouse reports keeps its
 *      right-clicks: the press and the release reach it as the pointer intents
 *      it enabled, and no menu of ours appears over its screen.
 *
 * CLIPBOARD REALITY: "pastes nothing" needs a clipboard with something in it,
 * or the assertion would hold on the unfixed product too. That means the
 * browser clipboard, which is a different store from the Wails runtime's, so
 * the runtime is disabled and the browser's is used — Chromium only, for the
 * permission Playwright grants there and nowhere else (clipboard.spec.ts's own
 * note). The gate runs chromium for this suite.
 */
import { expect, promptReady, test } from './harness'
import type { Locator } from '@playwright/test'
import type { Page } from './harness'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

const MENU = '[data-testid="block-actions-menu"]'
const ITEM = `${MENU} .ui-context-menu__item`
const COMPOSER = '.pane.active .nocx-editor-input'
const LIVE = '.pane.active .terminal-live-grid'
const LIVE_BOX = '.pane.active .terminal-live-container'

const nonce = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`
/** What the clipboard holds while a right-click happens. Short, and nothing a
 *  command prints, so finding it anywhere on screen is the paste itself. */
const MARKER = `RC-CLIP-${nonce}`

/** The browser's clipboard rather than the Wails runtime's (clipboard.ts). */
async function useBrowserClipboard(page: Page): Promise<void> {
  await page.addInitScript(() => {
    Object.defineProperty(window, 'runtime', {
      get() {
        return undefined
      },
      set(_value: unknown) {
        void _value
      },
      configurable: true,
      enumerable: true,
    })
  })
}

/** Put `text` on the clipboard the app will read. */
async function fillClipboard(page: Page, text: string): Promise<void> {
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.evaluate(async (value) => navigator.clipboard.writeText(value), text)
}

/** Right-click at a point inside `target`'s box — `dx` from its left edge,
 *  halfway down — and answer where the pointer was. */
async function rightClickInside(
  page: Page,
  target: Locator,
  dx = 40,
): Promise<{ x: number; y: number }> {
  const box = (await target.first().boundingBox())!
  const x = Math.round(box.x + dx)
  const y = Math.round(box.y + box.height / 2)
  await page.mouse.click(x, y, { button: 'right' })
  return { x, y }
}

/** The menu's rows, in order — the block's own list, whichever door opened it. */
async function menuRows(page: Page): Promise<string[]> {
  return page
    .locator(ITEM)
    .evaluateAll((rows) => rows.map((row) => (row as HTMLElement).dataset.itemId ?? ''))
}

/** Run one command and wait for its block to settle and the prompt to own
 *  input again. */
async function runCommand(page: Page, text: string): Promise<void> {
  await page.keyboard.type(text)
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .cmd-block:not(.cmd-block-running)').filter({ hasText: text }),
  ).toHaveCount(1, { timeout: 20_000 })
  await promptReady(page)
}

test('a finished block opens its ⋮ menu at the pointer, and the pane pastes nothing', async ({
  page,
  browserName,
}) => {
  test.skip(
    browserName !== 'chromium',
    'the clipboard this needs is Chromium-only (clipboard.spec.ts)',
  )
  await useBrowserClipboard(page)
  await page.goto('/')
  await expect(page.locator('.nocx-tab')).toHaveCount(1)
  await promptReady(page)

  await runCommand(page, `echo right-click-${nonce}`)
  const block = page
    .locator('.pane.active .cmd-block:not(.cmd-block-running)')
    .filter({ hasText: `echo right-click-${nonce}` })
    .last()
  const header = block.locator('.cmd-header')

  // THE ⋮'s OWN LIST, first — which is the list the pointer must produce.
  await block.locator('[data-block-actions]').click()
  await expect(page.locator(MENU)).toBeVisible()
  const fromButton = await menuRows(page)
  expect(fromButton).toContain('copy-command')
  await page.keyboard.press('Escape')
  await expect(page.locator(MENU)).toHaveCount(0)

  await fillClipboard(page, MARKER)
  const pointer = await rightClickInside(page, header)

  // AT THE POINTER: the kit's ContextMenu at the point it was given, not the
  // ⋮'s own corner (`context-menu.tsx` sets left/top from the anchor, through
  // the shared clamp — and this point is nowhere near an edge).
  const menu = page.locator(MENU)
  await expect(menu).toBeVisible()
  await expect(menu).toHaveCSS('left', `${pointer.x}px`)
  await expect(menu).toHaveCSS('top', `${pointer.y}px`)
  // THE SAME MENU, not a second list written for the pointer.
  expect(await menuRows(page)).toEqual(fromButton)

  // AND NOTHING WAS PASTED: the composer was where the old handler put the
  // clipboard, and it is empty of the marker.
  await expect(page.locator(COMPOSER)).not.toContainText(MARKER)
  await page.keyboard.press('Escape')
  await expect(page.locator(MENU)).toHaveCount(0)

  // ELSEWHERE IN THE PANE: a right-click on the live grid is nothing of the
  // pane's own — no menu, and still no paste.
  await rightClickInside(page, page.locator(LIVE_BOX), 30)
  await expect(page.locator(MENU)).toHaveCount(0)
  await expect(page.locator(COMPOSER)).not.toContainText(MARKER)
})

test('a running block offers its own menu — Stop — and the program is sent no paste', async ({
  page,
  browserName,
}) => {
  test.skip(
    browserName !== 'chromium',
    'the clipboard this needs is Chromium-only (clipboard.spec.ts)',
  )
  await useBrowserClipboard(page)
  await page.goto('/')
  await expect(page.locator('.nocx-tab')).toHaveCount(1)
  await promptReady(page)

  // A program that echoes what it is given, so the screen is the receipt: a
  // paste that reached it would be painted back by cat itself.
  await page.keyboard.type('cat -v')
  await page.keyboard.press('Enter')
  const running = page.locator('.pane.active .cmd-block-running').filter({ hasText: 'cat -v' })
  await expect(running).toHaveCount(1, { timeout: 20_000 })
  const grid = page.locator(LIVE)
  await expect(grid).toBeVisible()

  await fillClipboard(page, MARKER)
  await rightClickInside(page, running.locator('.cmd-header'))

  const menu = page.locator(MENU)
  await expect(menu).toBeVisible()
  // The liveness-bound row exists on a running block, which is what makes this
  // the block's OWN menu rather than a frozen list of copy rows.
  await expect(page.locator(`${ITEM}[data-item-id="stop"]`)).toBeVisible()

  // AND NOT SENT: nothing painted the marker back.
  await expect(page.locator(LIVE), 'the program was sent no paste').not.toContainText(MARKER)
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)

  // Leave the pane able to run its next command: a click puts the keyboard
  // back in the program's grid (the menu's Escape returned it wherever it
  // was), and Ctrl+C interrupts the running command.
  await grid.click()
  await page.keyboard.press('Control+c')
  await promptReady(page)
})

test('a full-screen program that reports the mouse keeps its right-clicks', async ({
  page,
  browserName,
}) => {
  test.skip(
    browserName !== 'chromium',
    'the clipboard this needs is Chromium-only (clipboard.spec.ts)',
  )
  await useBrowserClipboard(page)
  await page.goto('/')
  await expect(page.locator('.nocx-tab')).toHaveCount(1)
  await promptReady(page)

  // A real program, not a simulation: raw mode, the alternate screen, and the
  // three mouse modes a TUI enables, painting every byte it is sent back
  // (live-grid-drag-in-a-fullscreen-program.spec.ts's own fixture shape).
  const dir = mkdtempSync(join(tmpdir(), 'nocx-rc-tui-'))
  const script = join(dir, 'mouse-reporting.sh')
  writeFileSync(
    script,
    [
      '#!/bin/sh',
      'stty raw -echo',
      "printf '\\033[?1049h'",
      "printf '\\033[?1000h\\033[?1002h\\033[?1006h'",
      "printf '\\033[2J\\033[H'",
      `printf '\\033[3;1H%s mouse-reporting-job\\r\\n' '${MARKER}'`,
      "printf '\\033[6;1H'",
      'exec cat -v',
      '',
    ].join('\n'),
  )
  await page.keyboard.type(`sh ${script}`)
  await page.keyboard.press('Enter')
  const grid = page.locator(LIVE)
  await expect(page.locator(`${LIVE} .term-grid-row`, { hasText: MARKER })).toHaveCount(1, {
    timeout: 20_000,
  })
  await expect(page.locator(LIVE_BOX)).toHaveClass(/live-fullscreen/)

  await fillClipboard(page, `PASTE-${nonce}`)
  await rightClickInside(page, grid, 60)

  // THE PROGRAM GOT IT: the right button's press and release, in SGR 1006,
  // painted back by cat -v as its own receipt.
  await expect(grid).toContainText(/\[<2;\d+;\d+M/, { timeout: 10_000 })
  await expect(grid).toContainText(/\[<2;\d+;\d+m/, { timeout: 10_000 })
  // AND NOTHING OF OURS CAME OVER IT: no menu, and no paste on the screen.
  await expect(page.locator(MENU)).toHaveCount(0)
  await expect(grid).not.toContainText(`PASTE-${nonce}`)
})
