/**
 * Joined live-to-card selection and accessible reading acceptance
 * (nocx-zg3k3.4.3). This drives the real browser surface: a historical block,
 * the live grid, a frame arriving during pointer selection, clipboard output,
 * and keyboard navigation on both structures.
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
  await page.keyboard.type(`printf '${soft}-%*s\n' 180 '' | tr ' ' x; printf '%s\n' '${hard}'`)
  await page.keyboard.press('Enter')
  const card = page.locator('.cmd-block').filter({ hasText: soft }).last()
  await expect(card).toBeVisible()
  const cardRow = card.locator('.term-grid-row[data-block-id]').first()
  await expect(cardRow).toBeVisible()

  // Leave a real PTY reader running. Sending input to it during the drag
  // produces a frame on demand, without a timer or a timing-dependent wait.
  await promptReady(page)
  await page.keyboard.type(`printf '%s\n' '${live}'; cat`)
  await page.keyboard.press('Enter')
  const liveRow = page
    .locator('.pane.active .xterm-live-container .term-grid-row', { hasText: live })
    .last()
  await expect(liveRow).toBeVisible()
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
  const grid = page.locator('.pane.active .xterm-live-container [role="grid"]')
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
  await expect(page.locator('.pane.active .xterm-live-container')).toContainText(later)
  await page.keyboard.press('Control+C')
  await promptReady(page)
})
