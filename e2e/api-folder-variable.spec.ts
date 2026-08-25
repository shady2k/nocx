/**
 * e2e: a request inherits a folder variable when no environment is selected
 * (nocx-x3cax.5).
 *
 * The walk follows the product's doors: import a collection containing a folder,
 * open that folder, add and save a variable, open its request, send it, then
 * inspect the Variables tab. The server only answers 200 for the substituted
 * path, so a literal `{{folderValue}}` cannot satisfy the send assertion.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state: the
 * workbench, imported rows, the folder form, the run outcome, and the inherited
 * variable row.
 */
import { test as base, expect } from '@playwright/test'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { AddressInfo } from 'node:net'

import { bindEndpoint, VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'

const test = base

const COLLECTION_NAME = 'folder-variable-api'
const FOLDER_NAME = 'shared-values'
const REQUEST_NAME = 'get folder value'
const VARIABLE_NAME = 'folderValue'
const VARIABLE_VALUE = 'e2e-folder-value-42'

interface FolderVariableServer {
  readonly baseUrl: string
  paths(): readonly string[]
  stop(): Promise<void>
}

async function startFolderVariableServer(): Promise<FolderVariableServer> {
  const received: string[] = []
  const expectedPath = `/users/${VARIABLE_VALUE}`
  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const path = req.url ?? ''
    received.push(path)
    if (req.method !== 'GET' || path !== expectedPath) {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end('{"error":"folder variable was not substituted"}')
      return
    }
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end('{"ok":true}')
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })

  const port = (server.address() as AddressInfo).port
  return {
    baseUrl: `http://127.0.0.1:${port}`,
    paths: () => received,
    stop: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections()
        server.close(() => resolve())
      }),
  }
}

function folderVariableExport(baseUrl: string): string {
  const base = new URL(baseUrl)
  return JSON.stringify(
    {
      info: {
        _postman_id: '4e0cefa1-5d22-4e3c-8b1a-52f7b3c9d604',
        name: COLLECTION_NAME,
        schema: 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json',
      },
      item: [
        {
          name: FOLDER_NAME,
          item: [
            {
              name: REQUEST_NAME,
              request: {
                method: 'GET',
                header: [],
                url: {
                  raw: `${baseUrl}/users/{{${VARIABLE_NAME}}}`,
                  host: [base.hostname],
                  port: base.port || undefined,
                  path: ['users', `{{${VARIABLE_NAME}}}`],
                },
              },
              response: [],
            },
          ],
        },
      ],
    },
    null,
    2,
  )
}

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const devharnessBin = (): string => readStand().devharness

test.describe('a request inherits and sends a folder variable', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  let server: FolderVariableServer

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-folder-variable-')) }
    server = await startFolderVariableServer()
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterAll(async () => {
    backend?.stop()
    await server?.stop()
  })

  test('saves a folder variable, sends its request, and shows the inherited winner', async ({
    page,
  }) => {
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })

    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Import collection…' }).click()
    const importAsk = page.getByRole('dialog').filter({ hasText: 'Import collection' })
    await expect(importAsk).toBeVisible()
    await page.locator('#api-import-paste').fill(folderVariableExport(server.baseUrl))
    await expect(importAsk.getByRole('button', { name: 'Import', exact: true })).toBeEnabled()
    await importAsk.getByRole('button', { name: 'Import', exact: true }).click()

    const collectionRow = workbench.locator('.api-tree__row').filter({ hasText: COLLECTION_NAME })
    await expect(collectionRow).toBeVisible({ timeout: 15_000 })
    const folderRow = workbench.locator('.api-tree__row').filter({ hasText: FOLDER_NAME })
    await expect(folderRow).toBeVisible({ timeout: 15_000 })
    await expect(workbench.getByText('None yet. A URL written in', { exact: false })).toBeVisible()
    await folderRow.click()

    // THE FOLDER PAGE OPENS ON ITS CONTENTS (nocx-x3cax.6). The variables
    // editor is the strip's second section rather than the top of the page,
    // so the scope is reached here the way a person reaches it. The tab
    // counts its rows — `Variables` bare, `Variables 1` once there is one —
    // so it is matched on the word and not on the whole label.
    const folderPage = workbench.locator('.api-folder')
    await folderPage.getByRole('tab', { name: /^Variables\b/ }).click()

    // AN EMPTY SCOPE IS NOT AN EMPTY TABLE. `EditableRowList` draws its
    // `<table aria-label>` only once there is a row to put in it
    // (ui/row-list.tsx), so before `Add variable` there is a sentence saying
    // the folder declares nothing and the button that ends that — and the
    // button is reached from the page, not from inside a table that does not
    // exist yet. The old locator asked for `region`, which nothing here has
    // ever been: a labelled `<table>` is `table`.
    await expect(folderPage.getByText('No variables declared in this folder.')).toBeVisible()
    await folderPage.getByRole('button', { name: 'Add variable' }).click()

    const folderView = folderPage.getByRole('table', { name: 'Folder variables' })
    await expect(folderView).toBeVisible({ timeout: 15_000 })
    await folderView.locator('#api-folder-var-name-0').fill(VARIABLE_NAME)
    await folderView.locator('#api-folder-var-value-0').fill(VARIABLE_VALUE)
    // THERE IS NO SAVE (nocx-x3cax.7): the rows write themselves once typing
    // stops. The page states the two states that exist, so this waits on
    // `Saved` — the observable event that says the write landed — and never
    // on the coalescing interval. `exact` is what keeps `Saving…` from
    // satisfying it.
    await expect(folderView.locator('#api-folder-var-name-0')).toHaveValue(VARIABLE_NAME)
    await expect(folderPage.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

    const requestRow = workbench.locator('.api-tree__row').filter({ hasText: REQUEST_NAME })
    await expect(requestRow).toBeVisible({ timeout: 15_000 })
    await requestRow.click()
    await expect(workbench.locator('#api-url')).toHaveValue(
      `${server.baseUrl}/users/{{${VARIABLE_NAME}}}`,
    )

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    const run = workbench.locator('.api-run').first()
    await expect(run).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    await expect(run.locator('.api-run__stats')).toContainText('HTTP status 200')
    expect(server.paths()).toEqual([`/users/${VARIABLE_VALUE}`])

    await workbench.getByRole('tab', { name: 'Variables', exact: true }).click()
    const inherited = workbench.locator('table[aria-label="Inherited request variables"]')
    await expect(inherited).toBeVisible()
    const inheritedRow = inherited.locator('tr').filter({ hasText: VARIABLE_NAME })
    await expect(inheritedRow).toContainText(FOLDER_NAME)
    await expect(inheritedRow.locator('td').first()).toHaveText('folder')
  })
})
