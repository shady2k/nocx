/**
 * The field kit's layout contract: showing a trailing action must not move
 * the field, its neighboring field, or their row when focus reveals the action.
 */
import { test as base, expect, type Locator } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { openWorkbench, RATE_LIMIT, treeRow } from './api-workbench'
import { VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'

const test = base

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const serverBin = (): string => readStand().server

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
})
