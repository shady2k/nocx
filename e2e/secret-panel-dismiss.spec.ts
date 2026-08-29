/**
 * e2e: a secret picker dismisses through the document even after its field
 * loses focus. The field's own key handler cannot cover this state because
 * the document-owned FloatingPanel is the dismissal owner.
 */
import type { Locator } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { test, expect, VaultBackend, type DisposableRoot, type Page } from './harness'
import { openWorkbench, treeRow, ZEN } from './api-workbench'
import { readStand } from './stand'

const serverBin = (): string => readStand().server
const OPEN_PANEL = '.ui-floating-panel[data-variant="secret"][data-open="true"]'

/** Open the seeded request, then add the empty header used by the picker path. */
async function openHeaderField(
  page: Page,
  backend: VaultBackend,
): Promise<{ workbench: Locator; field: Locator }> {
  const workbench = await openWorkbench(page, backend)
  await treeRow(page, workbench, ZEN).click()
  const headers = workbench.getByRole('tab', { name: /^Headers(?: \d+)?$/ })
  await expect(headers, 'Zen request did not expose a Headers tab').toBeVisible({
    timeout: 10_000,
  })
  await headers.click()
  await workbench.getByRole('button', { name: 'Add header', exact: true }).click()
  const field = workbench.locator('input[id^="api-header-value-"]').last()
  await expect(field, 'newly added header value field did not appear').toBeVisible({
    timeout: 10_000,
  })
  return { workbench, field }
}

/** Type the trigger with a real caret, then wait for the mounted panel. */
async function triggerPanel(page: Page, field: Locator): Promise<void> {
  await field.click()
  await page.keyboard.type('@')
  await expect(page.locator(OPEN_PANEL)).toBeVisible({ timeout: 10_000 })
}

test.describe('secret picker document dismissal', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-secret-dismiss-')) }
    backend = new VaultBackend(serverBin(), disposable)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('Escape closes after focus moves away from the field', async ({ page }) => {
    const { workbench, field } = await openHeaderField(page, backend)
    await triggerPanel(page, field)

    const url = workbench.locator('#api-url')
    await url.focus()
    await expect(url).toBeFocused()

    await page.keyboard.press('Escape')
    await expect(
      page.locator(OPEN_PANEL),
      'Escape left the picker open after header value blur',
    ).toHaveCount(0)
    await expect(workbench, 'Escape removed the API workbench').toBeVisible()
  })

  test('pointerdown outside the panel closes it while the workbench stays open', async ({
    page,
  }) => {
    const { workbench, field } = await openHeaderField(page, backend)
    await triggerPanel(page, field)

    const url = workbench.locator('#api-url')
    await url.click()

    await expect(
      page.locator(OPEN_PANEL),
      'clicking the URL field did not dismiss the picker',
    ).toHaveCount(0)
    await expect(workbench, 'outside dismissal removed the API workbench').toBeVisible()
  })
})
