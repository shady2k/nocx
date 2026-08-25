/**
 * e2e: a Postman request-owned path variable resolves without an environment
 * (pathvar-7731).
 *
 * The whole check follows the user's path: import ask, collection tree row,
 * request row, Send. The server owns the decisive assertion by answering 200
 * only for the resolved path; a literal `:id` or `{{id}}` gets 404 instead.
 * The export has no collection-level variables, so the request's own `id` is
 * never created, selected, or edited through an environment.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state: the
 * workbench, the import dialog, the imported request row, and the run outcome.
 */
import { test as base, expect } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { bindEndpoint, VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'
import {
  startPathVariableServer,
  type PathVariableServer,
} from './fixtures/api-path-variable-server'
import {
  PATH_VARIABLE_COLLECTION_NAME,
  PATH_VARIABLE_ID,
  PATH_VARIABLE_REQUEST_NAME,
  pathVariableExport,
} from './fixtures/postman-path-variable'

const test = base

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const devharnessBin = (): string => readStand().devharness

test.describe('a request-owned path variable sends its resolved value', () => {
  // Three columns — tree, request, runs — must all be on screen for a person
  // to do this. The container's default viewport is narrower than the app's.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  let server: PathVariableServer

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-path-variable-')) }
    server = await startPathVariableServer({ expectedID: PATH_VARIABLE_ID })
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterAll(async () => {
    backend?.stop()
    await server?.stop()
  })

  test('imports, opens, and sends the request without an environment', async ({ page }) => {
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // The activity-bar entry is how a person reaches the API workbench.
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })
    await expect(workbench.locator('.api-tree__row').first()).toBeVisible({ timeout: 15_000 })

    // Import through the ask, with the request-owned variable in the export.
    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Import collection…' }).click()
    const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })
    await expect(ask).toBeVisible()
    await page.locator('#api-import-paste').fill(pathVariableExport(server.baseUrl))
    await expect(ask.getByRole('button', { name: 'Import', exact: true })).toBeEnabled()
    await ask.getByRole('button', { name: 'Import', exact: true }).click()

    // The imported collection and request rows are the observable completion
    // of the import and the handles a person uses for the next step.
    const requestRow = workbench
      .locator('.api-tree__row')
      .filter({ hasText: PATH_VARIABLE_REQUEST_NAME })
    await expect(requestRow).toBeVisible({ timeout: 15_000 })
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: PATH_VARIABLE_COLLECTION_NAME }),
    ).toBeVisible({ timeout: 15_000 })
    await requestRow.click()

    // The request is still unresolved in the editor; its own variable table is
    // what Send must resolve, not an environment chosen by this spec.
    await expect(workbench.locator('#api-url')).toHaveValue(`${server.baseUrl}/users/{{id}}`)

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    const run = workbench.locator('.api-run').first()
    await expect(run).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    await expect(run.locator('.api-run__stats')).toContainText('HTTP status 200')

    // The server route rejects both literal spellings and only answers 200 for
    // the resolved path. This is the acceptance proof, not a store assertion.
    expect(server.paths()).toEqual([`/users/${PATH_VARIABLE_ID}`])
  })
})
