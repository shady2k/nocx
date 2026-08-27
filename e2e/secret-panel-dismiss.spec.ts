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
import { openWorkbench, RATE_LIMIT, treeRow } from './api-workbench'
import { readStand } from './stand'

const devharnessBin = (): string => readStand().devharness
const OPEN_PANEL = '.ui-floating-panel[data-variant="secret"][data-open="true"]'

/** Open the seeded request whose Headers tab has a value field. */
async function openHeaderField(
  page: Page,
  backend: VaultBackend,
): Promise<{ workbench: Locator; field: Locator }> {
  const workbench = await openWorkbench(page, backend)
  await treeRow(page, workbench, RATE_LIMIT).click()
  await workbench.getByRole('tab', { name: 'Headers 1', exact: true }).click()
  const field = workbench.locator('#api-header-value-0')
  await expect(field).toBeVisible({ timeout: 10_000 })
  return { workbench, field }
}

/** Type the trigger with a real caret, then wait for the mounted panel. */
async function triggerPanel(page: Page, field: Locator): Promise<void> {
  await field.click()
  await field.pressSequentially('@')
  await expect(page.locator(OPEN_PANEL)).toBeVisible({ timeout: 10_000 })
}

test.describe('secret picker document dismissal', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-secret-dismiss-')) }
    backend = new VaultBackend(devharnessBin(), disposable, true)
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
