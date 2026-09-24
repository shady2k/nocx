import { test, expect, openControlPlane, promptReady } from './harness'
import { readStand } from './stand'

test('a running command block grows from backend-stored rows', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/')
  await promptReady(page)

  const nonce = Date.now().toString(36)
  const markers = Array.from(
    { length: 300 },
    (_, index) => `ROWS-${nonce}-${String(index + 1).padStart(3, '0')}`,
  )
  const firstMarker = markers[0]
  const finalMarker = markers[markers.length - 1]
  // The pause's own `read` is silent (-s): a `read -r` echoes the keystroke
  // that answers it, and that echo is genuine tty output for a keystroke
  // this SPEC injects to prove the block grows mid-run — not a boundary
  // artifact nocx-2v80t.3.12 is about. Reading it silently keeps the test's
  // own input off the stored output instead of asking the product to guess
  // which bytes were typed rather than printed.
  const command = `i=1; while [ "$i" -le 150 ]; do printf 'ROWS-${nonce}-%03d\\n' "$i"; i=$((i+1)); done; IFS= read -rs _; while [ "$i" -le 300 ]; do printf 'ROWS-${nonce}-%03d\\n' "$i"; i=$((i+1)); done`
  const input = page.locator('.pane.active .nocx-editor-input')
  await input.fill(command)
  await page.keyboard.press('Enter')

  const running = page.locator('.pane.active .cmd-block.cmd-block-running')
  await expect(running).toHaveCount(1, { timeout: 15_000 })
  const entryId = await running.getAttribute('data-entry-id')
  expect(entryId).toBeTruthy()
  const rows = page.locator(
    `.pane.active .cmd-block[data-entry-id="${entryId}"] .cmd-output .term-grid-row`,
  )
  await expect(running).toContainText(firstMarker, { timeout: 15_000 })
  const earlyCount = await rows.count()
  expect(earlyCount).toBeGreaterThan(1)
  await page.keyboard.press('Enter')
  await expect.poll(async () => rows.count(), { timeout: 10_000 }).toBeGreaterThan(earlyCount)
  await expect(page.locator('.pane.active .xterm-live-container')).toContainText(finalMarker, {
    timeout: 20_000,
  })

  await expect(running).toHaveCount(0, { timeout: 15_000 })
  const frozen = page.locator(`.pane.active .cmd-block[data-entry-id="${entryId}"]`)
  await expect(frozen).toBeVisible({ timeout: 15_000 })

  await expect(frozen).toContainText(firstMarker)
  await expect(frozen).toContainText(finalMarker)
  const frozenMarkers =
    (await frozen.locator('.cmd-output').textContent())?.match(
      new RegExp(`ROWS-${nonce}-\\d{3}`, 'g'),
    ) ?? []
  expect(frozenMarkers).toEqual(markers)
  await expect(frozen.locator('[data-output-incomplete]')).toHaveCount(0)

  const stand = readStand()
  const wire = await openControlPlane(stand.port, stand.token)
  try {
    const detail = (await wire.call('ledger.get', { id: entryId })) as {
      artifacts: Array<{
        mediaType: string
        id: string
        byteLen: number
        truncated: string | null
        payload: Record<string, unknown>
      }>
    }
    const rowsArtifact = detail.artifacts.find(
      (artifact) => artifact.mediaType === 'application/x-nocx-rows',
    )
    expect(rowsArtifact).toBeDefined()
    expect(rowsArtifact?.truncated).toBeNull()
    const body = (await wire.call('ledger.artifact', { id: rowsArtifact?.id })) as {
      body: string
    }
    // A row's own `text` (nocx-zg3k3.2.12) already is the flattened line —
    // one grapheme per surviving position, spacers and blanks contributing
    // nothing — and these marker rows carry no wide or blank cell, so it is
    // a plain substring rather than something rebuilt from a per-column
    // [grapheme, width, hasText] tuple array.
    const storedRows = body.body
      .split('\n')
      .filter(Boolean)
      .map((line) => JSON.parse(line) as { row: { text: string } })
      .map((line) => line.row.text)
    // The stored rows are EXACTLY the 300 markers — the shell's echo of the
    // command line and the next prompt are not painted as output either
    // (nocx-2v80t.3.12), so this is not a filtered subset check: any extra
    // row at either end fails it.
    expect(storedRows).toEqual(markers)
  } finally {
    wire.close()
  }

  await page.reload()
  await promptReady(page)
  const restored = page.locator(`.pane.active .cmd-block[data-entry-id="${entryId}"]`)
  await expect(restored).toBeVisible({ timeout: 15_000 })
  await expect(restored).toContainText(firstMarker)
  await expect(restored).toContainText(finalMarker)
  const restoredMarkers =
    (await restored.locator('.cmd-output').textContent())?.match(
      new RegExp(`ROWS-${nonce}-\\d{3}`, 'g'),
    ) ?? []
  expect(restoredMarkers).toEqual(markers)
  await expect(restored.locator('[data-output-incomplete]')).toHaveCount(0)
})
