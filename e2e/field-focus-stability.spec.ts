/**
 * The field kit's layout contract: revealing something inside a field must not
 * move the field, its neighbours, or the controls under it.
 *
 * TWO REVEALS, and the second one bought this file its second test. A trailing
 * action appears on focus; a validation message appears on blur. The message
 * is the expensive one, because blur fires on MOUSEDOWN: a form that grows a
 * line there reflows between the press and the release, the release lands on
 * whatever has moved under the pointer, and the browser then dispatches
 * `click` on the nearest common ancestor rather than on the button — so the
 * button's handler never runs and nothing at all happens. That is
 * `nocx-n26p1`, which spent three sessions being read as a lost event in
 * webkit: `+ Add header`, pressed as the first action in a dialog whose
 * required Name field is focused and empty, added no row about one run in
 * five.
 *
 * The kit already decided this for a captioned field — text-field.tsx keeps
 * ONE caption slot beneath the control, out of flow, holding the caption or
 * the error in its place, "so the field's height does not change when a value
 * goes out of range". A field with no caption fell through to Field's own
 * error line, which is IN flow. These tests state the contract for both.
 */
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { openWorkbench, RATE_LIMIT, treeRow } from './api-workbench'
import {
  appReadyForInput,
  bindEndpoint,
  settingsReady,
  VaultBackend,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

const test = base

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const serverBin = (): string => readStand().server

/** Settings → Endpoints → the New Endpoint dialog, with Name focused and
 *  empty — the state a person is in the moment the dialog opens. */
async function openNewEndpointDialog(page: Page, backend: VaultBackend): Promise<Locator> {
  const ep = await backend.start()
  await bindEndpoint(page, ep)
  await page.goto('/')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)

  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator('.ui-grouped-nav__item[data-item="endpoints"] button').click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: '+ New endpoint' }).first().click()

  const dialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
  await expect(dialog).toBeVisible({ timeout: 10_000 })
  return dialog
}

test.describe('field focus stability', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-field-focus-')) }
    backend = new VaultBackend(serverBin(), disposable)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('focusing a header value leaves its row and neighboring field in place', async ({
    page,
  }) => {
    const workbench = await openWorkbench(page, backend)
    await treeRow(page, workbench, RATE_LIMIT).click()
    await expect(workbench.locator('#api-url')).toHaveValue('{{baseUrl}}/rate_limit')
    await workbench.getByRole('tab', { name: 'Headers 1', exact: true }).click()

    const value = workbench.locator('#api-header-value-0')
    const name = workbench.locator('#api-header-name-0')
    const row = workbench.locator('table[aria-label="Request headers"] tbody tr').first()
    await expect(value).toBeVisible()
    await name.click()
    await expect(workbench.getByRole('button', { name: 'Store in vault' })).toHaveCount(0)

    const box = async (locator: Locator) => {
      const measured = await locator.boundingBox()
      expect(measured).not.toBeNull()
      return measured!
    }
    const before = {
      value: await box(value),
      name: await box(name),
      row: await box(row),
    }

    await value.click()
    await expect(value).toBeFocused()
    await expect(workbench.getByRole('button', { name: 'Store in vault' })).toBeVisible()

    expect(await box(value)).toEqual(before.value)
    expect(await box(name)).toEqual(before.name)
    expect(await box(row)).toEqual(before.row)
  })

  test('a required field revealing its message leaves the controls under it in place', async ({
    page,
  }) => {
    const dialog = await openNewEndpointDialog(page, backend)
    const name = dialog.locator('#endpoint-name')
    const addHeader = dialog.getByRole('button', { name: 'Add header' })
    await expect(name).toBeFocused()
    await expect(addHeader).toBeVisible()

    const box = async (locator: Locator) => {
      const measured = await locator.boundingBox()
      expect(measured).not.toBeNull()
      return measured!
    }
    const before = await box(addHeader)

    // Blur WITHOUT a pointer, so this test measures the reflow rather than
    // the swallowed click the reflow causes. `Tab` moves to Base URL, which
    // is what a person leaving an empty required field does.
    await name.press('Tab')
    await expect(dialog.getByText('Name is required')).toBeVisible()

    expect(
      await box(addHeader),
      'revealing the Name message moved the Add header button, so a press already in flight lands on nothing',
    ).toEqual(before)
  })

  test('the first press after opening the dialog adds a header row', async ({ page }) => {
    // The user-facing half of the same contract, through the seam a person
    // reaches: open the dialog, press Add header first, get a row. It is the
    // press itself that carries the risk — mousedown blurs Name and the
    // message it reveals is what used to move the button out from under the
    // release.
    const dialog = await openNewEndpointDialog(page, backend)
    await expect(dialog.locator('#endpoint-name')).toBeFocused()

    await dialog.getByRole('button', { name: 'Add header' }).click()

    await expect(dialog.locator('#endpoint-header-0-value')).toBeVisible({ timeout: 10_000 })
  })
})
