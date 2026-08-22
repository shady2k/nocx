/**
 * e2e: a secret anywhere in a request — the epic's DONE WHEN (nocx-ew3uv.4).
 *
 * The Telegram shape, end to end: a bot token that belongs in the PATH,
 * `/bot<TOKEN>/sendMessage`. Before this epic a vault-held value reached
 * exactly one field — the auth variable — so the only way to send this
 * request was to type the token into a file that goes into git.
 *
 * TWO ASSERTIONS CARRY THE WHOLE THING, and they pull in opposite directions
 * on purpose:
 *
 *   1. THE SERVER RECEIVED THE REAL VALUE, in the path. Its route is
 *      registered under the real token and everything else answers 404
 *      (fixtures/api-path-token-server.ts), so a request that carried the
 *      literal `{{token}}` cannot produce the 200 this spec waits for. A
 *      check that only read the run would pass on exactly that request.
 *   2. AND NO BYTE OF IT REACHED THE SCREEN. Asserted on `page.content()` —
 *      the whole serialised document, attributes included — because a
 *      credential can ride a title, a data-* or a hidden node that no text
 *      assertion would ever look at.
 *
 * THE BINDING IS MINTED THROUGH THE PRODUCT. A Postman export whose
 * collection variable is `type: "secret"` puts the VALUE in the vault and
 * leaves the NAME in the environment file — a path a person takes, and the
 * one the panel's own write (nocx-ew3uv.1) will join once its two renderer
 * halves are wired. Nothing here calls the control plane to arrange a state
 * the product is supposed to arrange.
 *
 * ITS OWN BACKEND, like api-testing.spec.ts and for the same two reasons: the
 * import writes a secret VALUE, so this run needs a vault it set up itself
 * rather than leaving one behind for every other spec — and the walk in step
 * 3 reads the collection folder off the isolated home this backend resolved.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state: a
 * dialog, a directory on disk, a row in the tree, a run in the list.
 */
import { test as base, expect } from '@playwright/test'
import {
  existsSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { bindEndpoint, settingsReady, VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'
import {
  SENT_MESSAGE_BODY,
  startPathTokenServer,
  type PathTokenServer,
} from './fixtures/api-path-token-server'
import {
  TELEGRAM_BOT_TOKEN,
  TELEGRAM_COLLECTION_NAME,
  TELEGRAM_REQUEST_NAME,
  telegramExport,
} from './fixtures/postman-export'

const test = base

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const devharnessBin = (): string => readStand().devharness

/** The vault passphrase this run sets up. It is a passphrase, not a token. */
const VAULT_PASSPHRASE = 'api-secret-path-e2e-master-pass'

/** Every file under `root`, recursively — the §8 walk. */
function walk(root: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(root)) {
    const full = join(root, entry)
    if (statSync(full).isDirectory()) out.push(...walk(full))
    else out.push(full)
  }
  return out
}

test.describe('a secret in the path: the value crosses to the server and never to the screen', () => {
  // Three columns — tree, request, runs — must all be on screen for a person
  // to do this. The container's default viewport is narrower than the app's.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  let server: PathTokenServer
  let exportPath: string
  let collectionRoot: string

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-secret-path-')) }
    server = await startPathTokenServer({ expectedToken: TELEGRAM_BOT_TOKEN })
    exportPath = join(disposable.root, 'telegram.postman_collection.json')
    writeFileSync(exportPath, telegramExport(server.baseUrl), 'utf8')
    // It must NOT exist beforehand: an import refuses an occupied
    // destination rather than replacing it (§12.2).
    collectionRoot = join(disposable.root, TELEGRAM_COLLECTION_NAME)
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterAll(async () => {
    backend?.stop()
    await server?.stop()
  })

  test('a token in the URL path is sent for real and shown nowhere', async ({ page }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // ── The vault, because the import has somewhere to put the token ──────
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="secrets"]').click()
    await page.getByRole('button', { name: 'Set up protection' }).click()
    await expect(page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })).toBeVisible({
      timeout: 10_000,
    })
    await page.locator('#vault-setup-passphrase').fill(VAULT_PASSPHRASE)
    await page.locator('#vault-setup-confirm').fill(VAULT_PASSPHRASE)
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()
    await expect(page.getByRole('dialog').filter({ hasText: 'Recovery Code' })).toBeVisible({
      timeout: 10_000,
    })
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    // ── The workbench, and the export imported through it ─────────────────
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })

    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Import collection…' }).click()
    await expect(page.locator('#api-import-postman-file')).toBeVisible()
    await page.locator('#api-import-postman-file').fill(exportPath)
    await page.locator('#api-import-postman-dest').fill(collectionRoot)
    await page.getByRole('button', { name: 'Import', exact: true }).click()

    // The folder arriving is the import's closing event (§12.2), so waiting
    // on it is waiting on a state rather than on a duration.
    await expect
      .poll(() => existsSync(collectionRoot), {
        message: `the import never produced ${collectionRoot}`,
        timeout: 15_000,
      })
      .toBe(true)

    // ── The folder is safe to commit — the whole folder, not one file ─────
    const files = walk(collectionRoot)
    expect(files.length, 'the import wrote no files').toBeGreaterThan(0)
    for (const file of files) {
      expect(readFileSync(file).toString('utf8'), `${file} carries the token`).not.toContain(
        TELEGRAM_BOT_TOKEN,
      )
    }
    // …and it DECLARES the variable, or the absence above would pass on an
    // import that dropped it. The name is in the file; the value is not.
    const env = readFileSync(join(collectionRoot, 'environments', 'default.json')).toString('utf8')
    expect(JSON.parse(env) as { secretVars?: string[] }).toMatchObject({ secretVars: ['token'] })

    // ── Open it, and send ─────────────────────────────────────────────────
    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Open folder…' }).click()
    const folderAsk = page.getByRole('dialog').filter({ hasText: 'Open a collection folder' })
    await expect(folderAsk).toBeVisible()
    await page.locator('#api-collection-path').fill(collectionRoot)
    await folderAsk.getByRole('button', { name: 'Open', exact: true }).click()

    const requestRow = workbench
      .locator('.api-tree__row')
      .filter({ hasText: TELEGRAM_REQUEST_NAME })
    await expect(requestRow).toBeVisible({ timeout: 10_000 })
    await requestRow.click()

    // The form shows the file, UNRESOLVED: the file is the truth and the
    // form is a projection of it (§6.4). This is the address a person sees,
    // and the token is not in it.
    await expect(workbench.locator('#api-url')).toHaveValue('{{baseUrl}}/bot{{token}}/sendMessage')

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()

    const run = workbench.locator('.api-run').first()
    await expect(run).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })

    // ── 1. THE SERVER RECEIVED THE REAL VALUE ─────────────────────────────
    //
    // 200 is the credential check: the route is registered under the real
    // token's path and everything else is a 404, so this number cannot be
    // reached by a request carrying `{{token}}`.
    await expect(run.locator('.api-run__stats')).toContainText('HTTP status 200')
    await expect(run.locator('[aria-label="Response body"]')).toContainText(SENT_MESSAGE_BODY)
    // Said a second way, from the server's own record.
    expect(server.paths()).toEqual([`/bot${TELEGRAM_BOT_TOKEN}/sendMessage`])

    // ── 2. AND NO BYTE OF IT REACHED THE SCREEN ───────────────────────────
    //
    // The raw view first, because that is the surface whose entire purpose
    // is to show everything — if the value survives anywhere, it is here.
    await run.getByRole('tab', { name: 'Raw' }).click()
    const raw = run.locator('.api-run__raw')
    await expect(raw).toBeVisible()
    // The request line really is there, so "no token in the DOM" is not
    // passing because the pane is empty.
    await expect(raw.locator('[aria-label="Raw request"]')).toContainText('/sendMessage')
    // The badge NAMES the secret where its bytes were (§11.1).
    await expect(raw.locator('.ui-secret-chip__name').first()).toHaveText('token')

    // The whole serialised document, attributes included: a chip beside a
    // leak is still a leak, and a value can ride a title or a data-* that no
    // text assertion would look at.
    expect(await page.content(), 'the token reached the renderer in the clear').not.toContain(
      TELEGRAM_BOT_TOKEN,
    )
  })
})
