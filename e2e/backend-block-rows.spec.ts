import { test, expect, promptReady } from './harness'

test('a running command block grows from backend-stored rows', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/')
  await promptReady(page)

  const nonce = Date.now().toString(36)
  const firstMarker = `ROWS-${nonce}-001`
  const finalMarker = `ROWS-${nonce}-300`
  const command = `i=1; while [ "$i" -le 300 ]; do printf 'ROWS-${nonce}-%03d\\n' "$i"; i=$((i+1)); sleep 0.01; done; cat`
  const input = page.locator('.pane.active .nocx-editor-input')
  await input.fill(command)
  await page.keyboard.press('Enter')

  const running = page.locator('.pane.active .cmd-block.cmd-block-running')
  await expect(running).toHaveCount(1, { timeout: 15_000 })
  const rows = running.locator('.cmd-output .term-grid-row')
  await expect(running).toContainText(firstMarker, { timeout: 15_000 })
  const earlyCount = await rows.count()
  expect(earlyCount).toBeGreaterThan(1)
  await expect.poll(async () => rows.count(), { timeout: 10_000 }).toBeGreaterThan(earlyCount)

  await expect(page.locator('.pane.active .xterm-live-container')).toContainText(finalMarker, {
    timeout: 20_000,
  })

  await page.keyboard.press('Control+C')
  await expect(running).toHaveCount(0, { timeout: 15_000 })
  await page.reload()
  await promptReady(page)
  const restored = page.locator('.pane.active .cmd-block', { hasText: finalMarker }).first()
  await expect(restored).toBeVisible({ timeout: 15_000 })
  const restoredRows = restored.locator('.cmd-output .term-grid-row')
  await expect
    .poll(async () => restoredRows.count(), { timeout: 15_000 })
    .toBeGreaterThan(earlyCount)
  await expect(restored).toContainText(/Output incomplete: \d+ rows missing/)
})
