/**
 * e2e: the environments rail keeps a choice made while no request is open
 * (nocx-1ebrs.46).
 *
 * The rail's rows render from the COLLECTION LISTING, so they are clickable
 * before any request is open — and in particular in the window after a person
 * has clicked a request row while `openRequest`'s read is still in flight.
 * One CI webkit sighting failed exactly there: the click landed while no
 * draft existed, `setEnvironment` dropped it, and the single imported
 * environment's fallback owned the rail forever after. The store now holds
 * the choice for the request being opened; this spec watches a person do the
 * two halves of that journey through the surfaces they reach.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable: a row in
 * the tree, a value in the address field, an attribute on the rail button.
 */
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { appReadyForInput, test, expect } from './harness'

const REQUEST = 'env-rail'
const COLLECTION = 'rail-coll'
/** A name reserved never to resolve (RFC 2606); nothing here is sent. */
const URL = 'http://nocx-e2e-no-such-host.invalid/rail'

/**
 * A collection folder in the product's own on-disk format, holding ONE
 * environment — the same shape a Postman import with a collection variable
 * produces, and therefore the shape whose single-environment fallback the
 * sighting fought: with no opinion of its own, an open request shows the one
 * environment as selected.
 */
function writeCollection(root: string): void {
  mkdirSync(root, { recursive: true })
  const write = (rel: string, body: unknown): void =>
    writeFileSync(join(root, rel), JSON.stringify(body), 'utf8')
  write('nocx-collection.json', { schemaVersion: 1, name: COLLECTION })
  write(`${REQUEST}.json`, {
    id: 'r-env-rail',
    name: REQUEST,
    method: 'GET',
    url: URL,
    headers: [],
    query: [],
    body: { kind: 'none' },
    auth: { kind: 'none' },
  })
  mkdirSync(join(root, 'environments'), { recursive: true })
  write('environments/dev.json', {
    name: 'dev',
    values: { baseUrl: URL },
    route: { kind: 'direct' },
  })
}

test.describe('the environments rail keeps a choice made beside the form', () => {
  // Three columns must be on screen; the container's default viewport is
  // narrower than the shipped app's window.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: string
  let collectionRoot: string

  test.beforeAll(async () => {
    disposable = mkdtempSync(join(tmpdir(), 'nocx-e2e-rail-hold-'))
    collectionRoot = join(disposable, COLLECTION)
    writeCollection(collectionRoot)
  })

  test.afterAll(async () => {
    rmSync(disposable, { recursive: true, force: true })
  })

  /** The workbench with the collection open, its request row on screen. */
  async function openWorkbench(
    page: import('@playwright/test').Page,
  ): Promise<import('@playwright/test').Locator> {
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })
    await appReadyForInput(page)
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })
    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Open folder…' }).click()
    const folderAsk = page.getByRole('dialog').filter({ hasText: 'Open a collection folder' })
    await expect(folderAsk).toBeVisible()
    await page.locator('#api-collection-path').fill(collectionRoot)
    await folderAsk.getByRole('button', { name: 'Open', exact: true }).click()
    const row = workbench.locator('.api-tree__row').filter({ hasText: REQUEST })
    await expect(row).toBeVisible({ timeout: 10_000 })
    return workbench
  }

  test('a No-environment click made before the request opened still owns the rail (nocx-1ebrs.46)', async ({
    page,
  }) => {
    const workbench = await openWorkbench(page)
    const rail = workbench.locator('.api-environments-rail')
    const noEnvironment = rail.getByRole('button', { name: 'No environment', exact: true })
    await expect(noEnvironment).toBeVisible()

    // The click with NO request open — the store state the CI click saw while
    // the request's read was still in flight. The single environment is what
    // the rail selects by fallback, so this gesture is the only thing that
    // can move it.
    await noEnvironment.click()
    await expect(noEnvironment).toHaveAttribute('aria-selected', 'true')

    // Open the request and wait on its own observable: the address field
    // holding the file's URL. The held choice must own the rail afterwards.
    const row = workbench.locator('.api-tree__row').filter({ hasText: REQUEST })
    await row.click()
    await expect(workbench.locator('#api-url')).toHaveValue(URL, { timeout: 10_000 })
    await expect(noEnvironment).toHaveAttribute('aria-selected', 'true')
  })

  test('the click after the open still owns the rail, and a remembered file keeps its own', async ({
    page,
  }) => {
    const workbench = await openWorkbench(page)
    const rail = workbench.locator('.api-environments-rail')
    const noEnvironment = rail.getByRole('button', { name: 'No environment', exact: true })
    await expect(noEnvironment).toBeVisible()

    // The ordinary order: open, wait for the opened observable, then choose.
    const row = workbench.locator('.api-tree__row').filter({ hasText: REQUEST })
    await row.click()
    await expect(workbench.locator('#api-url')).toHaveValue(URL, { timeout: 10_000 })
    await noEnvironment.click()
    await expect(noEnvironment).toHaveAttribute('aria-selected', 'true')

    // And the choice reached the FILE, which is what a send reads: reopening
    // the same request keeps No environment without anybody clicking again.
    await row.click()
    await expect(workbench.locator('#api-url')).toHaveValue(URL, { timeout: 10_000 })
    await expect(noEnvironment).toHaveAttribute('aria-selected', 'true')
  })
})
