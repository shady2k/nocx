/** A secret picker row outside a connection dialog remains clickable. */
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import type { Locator } from '@playwright/test'
import {
  appReadyForInput,
  bindEndpoint,
  readVaultState,
  test,
  expect,
  settingsReady,
  type BackendEndpoint,
  type Page,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { activateRow } from './secret-field'

const SETTINGS_CONNECTIONS_NAV = '.ui-grouped-nav__item[data-item="connections"]'
const OPEN_PANEL = '.ui-floating-panel[data-variant="secret"][data-open="true"]'

let backend: VaultBackend | undefined
let endpoint: BackendEndpoint | undefined

function currentEndpoint(): BackendEndpoint {
  if (endpoint === undefined) throw new Error('secret panel backend was not started')
  return endpoint
}

test.beforeEach(async () => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-secret-panel-position-'))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()
})

test.afterEach(() => {
  backend?.stop()
  backend = undefined
  endpoint = undefined
})

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, currentEndpoint())
  await page.goto('/')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

/** The ordinary connection editor with its password field visible. */
async function openConnectionDialog(page: Page): Promise<Locator> {
  await openApp(page)
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(SETTINGS_CONNECTIONS_NAV).click()
  await expect(page.locator('.cm-root')).toBeVisible({ timeout: 10_000 })
  await page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }).click()

  const quickConnect = page.getByRole('dialog').filter({ hasText: 'New Connection' })
  await expect(quickConnect).toBeVisible({ timeout: 10_000 })
  await quickConnect.getByLabel('Host or connection string').fill('localhost')
  await quickConnect.getByRole('button', { name: 'Next', exact: true }).click()
  await expect(page.locator('.cm-form')).toBeAttached({ timeout: 10_000 })

  await page.locator('#profile-name').fill('Panel position test')
  await page.locator('#profile-host').fill('localhost')
  await page.locator('.cm-form').getByRole('tab', { name: 'Authentication' }).click()
  await page.locator('#profile-auth-user').fill('test-user')
  await page
    .getByRole('radiogroup', { name: 'Authentication method' })
    .getByRole('radio', { name: 'Password' })
    .click()

  const dialog = page
    .locator('dialog.nocx-dialog')
    .filter({ has: page.locator('#profile-auth-password') })
  await expect(dialog).toBeVisible({ timeout: 10_000 })
  return dialog
}

/** Type a bare '@' into a field, the way a person does — never `fill`, which
 *  sets the value without the caret the adapter reads.
 *
 *  The panel opens on a "loading secrets…" placeholder, which is a
 *  `.ui-floating-panel__row` too, and is re-rendered once vault.status
 *  answers: a taller panel with an offer row and a footer, re-placed above
 *  its field. Geometry read before that answer is the placeholder's, and a
 *  press aimed at it lands on whatever the settled panel puts there — the
 *  footer, as CI saw. So wait, as a person does, for the panel to stop
 *  loading. */
async function triggerPanel(page: Page, field: Locator): Promise<Locator> {
  await field.click()
  await field.pressSequentially('@')
  const panel = page.locator(OPEN_PANEL)
  await expect(panel).toBeVisible({ timeout: 10_000 })
  await expect(panel.getByText('loading secrets…')).toHaveCount(0, { timeout: 10_000 })
  return panel
}

/** Dismiss the panel the way the adapter does: the trigger word is the '@',
 *  and deleting it is what "I am not naming a secret" looks like. */
test.describe('the secret panel opens where a person can reach it', () => {
  test('a row still activates from the panel portion hanging past the dialog', async ({ page }) => {
    const vaultState = await readVaultState(currentEndpoint())
    expect(
      vaultState,
      `the overhang activation test requires an uninitialized vault; got ${vaultState}`,
    ).toBe('uninitialized')
    const dialog = await openConnectionDialog(page)
    const field = dialog.locator('#profile-auth-password')
    const panel = await triggerPanel(page, field)
    const row = panel.locator('.ui-floating-panel__row').first()

    // The panel is re-homed into the dialog so it can paint in the top layer.
    // Keep this assertion: without the DOM relationship, this test would not
    // prove the dialog's bubbling light-dismiss path.
    expect(
      await panel.evaluate((el) => el.closest('dialog') !== null),
      'secret picker panel was not re-homed into the connection dialog',
    ).toBe(true)

    const rowBox = await row.boundingBox()
    const dialogPanel = dialog.locator('.nocx-dialog__panel')
    const dialogPanelBox = await dialogPanel.boundingBox()
    const dialogSize = await dialogPanel.getAttribute('data-size')
    expect(rowBox, 'secret picker row has no measurable bounding box').not.toBeNull()
    expect(dialogPanelBox, 'connection dialog panel has no measurable bounding box').not.toBeNull()
    if (rowBox === null || dialogPanelBox === null) throw new Error('missing geometry')

    // Use the intersection of the row and the region immediately past the
    // dialog panel's right boundary, rather than a viewport constant. This
    // point is inside the row but outside the dialog panel.
    const dialogRight = dialogPanelBox.x + dialogPanelBox.width
    const hangingLeft = Math.max(rowBox.x, dialogRight)
    const hangingRight = rowBox.x + rowBox.width
    const diagnostic = [
      'the password picker row has no panel overhang',
      `viewport=${await page.evaluate(() => `${window.innerWidth}x${window.innerHeight}`)}`,
      `dialog data-size=${dialogSize ?? 'missing'}`,
      `dialog=${dialogPanelBox.x}..${dialogRight} (${dialogPanelBox.width}px)`,
      `row=${rowBox.x}..${hangingRight} (${rowBox.width}px)`,
      'expected ordinary dialog width 480px and secret panel min-width 480px',
    ].join('; ')
    expect(hangingRight, diagnostic).toBeGreaterThan(hangingLeft)
    const x = hangingLeft + (hangingRight - hangingLeft) / 2
    const y = rowBox.y + rowBox.height / 2

    await activateRow(page, row, { x, y })

    // The clicked offer row opens the vault setup surface. The connection
    // dialog must remain open underneath it; a light-dismiss would discard it.
    await expect(
      dialog,
      'activating the overhanging row dismissed the connection dialog',
    ).toBeVisible()
    const setupSheet = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-setup-passphrase') })
    await expect(
      setupSheet,
      'activating the overhanging row did not open the vault setup sheet',
    ).toBeVisible()
  })
})
