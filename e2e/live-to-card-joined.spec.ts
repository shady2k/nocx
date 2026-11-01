/**
 * Joined live-to-card selection and accessible reading acceptance
 * (nocx-zg3k3.4.3). This drives the real browser surface: a historical block,
 * the live grid, a frame arriving during pointer selection, clipboard output,
 * and keyboard navigation on both structures. It also exercises key, paste,
 * and IME input on the live terminal surface (nocx-zg3k3.8).
 */
import { test, expect, promptReady } from './harness'

const marker = (name: string): string => `JOIN-${name}-${Date.now().toString(36)}`

async function disableWailsRuntime(page: import('@playwright/test').Page): Promise<void> {
  await page.addInitScript(() => {
    Object.defineProperty(window, 'runtime', {
      get: () => undefined,
      set: (_value: unknown) => void _value,
      configurable: true,
    })
  })
}

async function composeTerminalInput(
  page: import('@playwright/test').Page,
  committed: string,
): Promise<void> {
  await page.locator('.pane.active .ui-terminal-input').evaluate((node, value) => {
    const input = node as HTMLTextAreaElement
    input.dispatchEvent(new CompositionEvent('compositionstart', { data: '', bubbles: true }))
    input.dispatchEvent(new CompositionEvent('compositionupdate', { data: value, bubbles: true }))
    input.dispatchEvent(
      new InputEvent('input', {
        inputType: 'insertCompositionText',
        data: value,
        isComposing: true,
        bubbles: true,
      }),
    )
    input.dispatchEvent(new CompositionEvent('compositionend', { data: value, bubbles: true }))
  }, committed)
}

test('a drag from a historical card to live output copies the pinned text and keeps the accessible screen current', async ({
  page,
  browserName,
}) => {
  test.skip(browserName !== 'chromium', 'clipboard-read permission is Chromium-only')
  await disableWailsRuntime(page)
  await page.goto('/')
  await promptReady(page)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])

  const soft = marker('SOFT')
  const hard = marker('HARD')
  const live = marker('LIVE')
  const later = marker('FRAME')
  // The long first line wraps in the terminal grid (soft join); the second
  // line is a hard line boundary. The completed output is a historical card.
  // Generate the long output in the shell instead of typing a long command
  // into the PTY, where key-rate limits could truncate the test fixture.
  await page.keyboard.type(`printf '${soft}-%*s\\n' 180 '' | tr ' ' x; printf '%s\\n' '${hard}'`)
  await page.keyboard.press('Enter')
  // THE FINISHED CARD, named as such (nocx-zg3k3.14.26, ADR-0091). While the
  // second command runs its own block holds the LIVE REGION, and the live
  // region paints the screen — which still carries the first command's rows.
  // So a text search over `.cmd-block` finds the running block too, and it is
  // the finished one this drag starts from. The same spelling the ask gesture
  // reads a historical block by (`ask-gesture.ts`).
  const card = page.locator('.cmd-block:not(.cmd-block-running)').filter({ hasText: soft }).last()
  await expect(card).toBeVisible()
  const cardRow = card.locator('.term-grid-row[data-block-id]').first()
  await expect(cardRow).toBeVisible()

  // Leave a real PTY reader running. Sending input to it during the drag
  // produces a frame on demand, without a timer or a timing-dependent wait.
  await promptReady(page)
  await page.keyboard.type(`printf '%s\\n' '${live}'; cat`)
  await page.keyboard.press('Enter')
  const grid = page.locator('.pane.active [role="grid"][aria-label="Terminal output"]')
  // THE PRINTED OUTPUT ROW, NOT THE SHELL'S ECHO OF THE COMMAND (nocx-atpw5.1.18).
  // `live` is part of the typed command too, so a plain hasText filter matches
  // the echo row first — and once the backend anchor places the output the
  // running region translates that echo row ABOVE its own clip
  // (controller._echoShiftPx, ADR-3kqrem7v). Playwright's visibility check does
  // not look through an ancestor's overflow, so the echo row read as "visible"
  // while its centre sat inside the 32px running header; the click then waited
  // forever on a point the header legitimately owns (ci run 41315097338, job
  // 124873463432). This row's own text is the printed marker: the row a person
  // actually sees and clicks.
  const liveRow = grid
    .locator('[role="row"]')
    .filter({ hasText: new RegExp(`^\\s*${live}\\s*$`) })
    .last()
  await expect(liveRow).toBeVisible()
  const terminalInput = page.locator('.pane.active .ui-terminal-input')
  // THE ROW A PERSON CLICKS, NOT THE ELEMENT'S CENTRE. The grid element is the
  // whole SCREEN grid (every row the pty was sized to), while the running
  // region clips it to the output tail; the element's own centre is therefore
  // outside the clip and a synthetic click there is intercepted by the header
  // or the scroller above it. The output row above is inside the clip and its
  // centre is below the header, so the click is the gesture ADR-0092 promises.
  // The assertion after it is unchanged: the click must reach the terminal
  // input.
  await liveRow.click()
  await expect(terminalInput).toBeFocused()

  const typed = marker('TYPE')
  await page.keyboard.type(typed)
  await page.keyboard.press('Enter')
  await expect(grid).toContainText(typed)

  const pasted = marker('PASTE')
  await page.evaluate((text) => navigator.clipboard.writeText(text), pasted)
  await page.keyboard.press('Control+V')
  await page.keyboard.press('Enter')
  await expect(grid).toContainText(pasted)

  const composed = marker('IME')
  await composeTerminalInput(page, composed)
  await page.keyboard.press('Enter')
  await expect(grid).toContainText(composed)

  const sourceBox = await cardRow.boundingBox()
  const targetBox = await liveRow.boundingBox()
  expect(sourceBox).not.toBeNull()
  expect(targetBox).not.toBeNull()

  const revisionBeforeDrag = await page.evaluate(() => window.__nocxPaneScreen?.()?.revision ?? -1)
  const start = { x: sourceBox!.x + 4, y: sourceBox!.y + sourceBox!.height / 2 }
  const end = { x: targetBox!.x + targetBox!.width * 0.8, y: targetBox!.y + targetBox!.height / 2 }
  await page.mouse.move(start.x, start.y)
  await page.mouse.down()
  await page.mouse.move((start.x + end.x) / 2, (start.y + end.y) / 2, { steps: 12 })
  await expect(page.locator('.term-grid-selection').first()).toBeVisible()
  // Typing into the running `cat` is a user action that deterministically
  // creates a real backend frame while the selection remains active.
  //
  // AND THE DRAG SURVIVES BOTH THE KEYS AND THAT FRAME (nocx-1ebrs.19). A range
  // still under the pointer is not a selection yet: the person has not let go,
  // and the release is where it is copied — so a key sent to the program
  // mid-drag ends nothing here, and neither does the repaint it causes. Ending
  // it was the regression: the clipboard kept `pasted` from the step above,
  // which reads exactly like a drag that copied nothing, because that is what
  // it was. The copy that follows is taken from the snapshot pinned at
  // pointerdown, which is why `later` is not in it.
  await page.keyboard.type(later)
  await page.keyboard.press('Enter')
  await expect
    .poll(() =>
      page.evaluate((revision) => {
        const reading = window.__nocxPaneScreen?.()
        return (
          (reading?.revision ?? -1) > revision && (reading?.rows.join('\n') ?? '').includes('FRAME')
        )
      }, revisionBeforeDrag),
    )
    .toBe(true)
  await page.mouse.move(end.x, end.y, { steps: 12 })
  await page.mouse.up()

  // The copied card half demonstrates soft-wrap joins and preserves the hard
  // newline. The live half is sourced from the snapshot pinned at pointerdown.
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain(soft)
  const copied = await page.evaluate(() => navigator.clipboard.readText())
  expect(copied).toContain(`${soft}-${'x'.repeat(180)}\n${hard}\n`)
  expect(copied).toContain(live)
  expect(copied).not.toContain(later)

  // Both structures are navigable on the same surface. No aria-live channel
  // means routine frame revisions cannot repeatedly speak dirty cells.
  await expect(grid).toHaveAttribute('aria-label', 'Terminal output')
  await expect(grid.locator('[role="row"]').first()).toHaveAttribute('aria-rowindex', '1')
  await expect(grid.locator('[aria-live]')).toHaveCount(0)
  const firstRow = grid.locator('[role="row"]').first()
  await firstRow.focus()
  await page.keyboard.press('ArrowDown')
  await expect(grid.locator('[aria-current="true"]')).toHaveCount(1)
  await expect(grid.locator('[aria-current="true"]')).toBeFocused()
  await expect(card).toHaveAttribute('role', 'group')
  await expect(card).toHaveAttribute('tabindex', '0')
  // Tab traversal from the adjacent toolbar reaches a command block group
  // without a pointer; the block remains a reading stop, not an activation.
  await page.getByRole('button', { name: 'More', exact: true }).focus()
  await page.keyboard.press('Tab')
  await expect(page.locator('.cmd-block:focus')).toHaveAttribute('role', 'group')

  // Once released, the newest screen remains visible. Stop the deliberately
  // interactive test process after observing its latest output.
  await expect(grid).toContainText(later)
  await page.keyboard.press('Control+C')
  await promptReady(page)
})
