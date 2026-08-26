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
 * ITS OWN BACKEND, like `api-import.spec.ts`, and for the same two reasons:
 * the import writes no credential value, while the picker-created value lives
 * in the vault; and the walk in step 3 reads the collection folder off the
 * isolated home this backend resolved.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state: a
 * dialog, a directory on disk, a row in the tree, a run in the list.
 */
import { test as base, expect } from '@playwright/test'
import { existsSync, mkdtempSync, readdirSync, readFileSync, statSync } from 'node:fs'
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
  let collectionRoot: string

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-secret-path-')) }
    server = await startPathTokenServer({ expectedToken: TELEGRAM_BOT_TOKEN })
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
    const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })
    await expect(ask).toBeVisible()

    // ── THE EXPORT, AS TEXT AND NOT AS A PATH ─────────────────────────────
    //
    // The ask stopped naming two absolute paths (nocx-ysyy2), and on a build
    // with no Wails neither gesture that ANSWERS with a path exists: no
    // system picker, no native window drop. What is left is the ask's one
    // question, and the export's own text is a source it takes — bytes reach
    // the backend wherever it runs, where a path only names a file on the
    // machine running Go (spec §1a).
    //
    // ASSERTION 2 IS UNAFFECTED by the change of entrance, and it is worth
    // saying why rather than hoping. Every entrance this build has hands the
    // renderer the BYTES: a chosen file is read with `File.text()` and sent
    // as a document too (api-pane.tsx). What that assertion watches is the
    // serialised DOM, and the box holds what was pasted as a property rather
    // than as markup — so it says exactly what it always said, that nothing
    // nocx RENDERS carries the value.
    await page.locator('#api-import-paste').fill(telegramExport(server.baseUrl))
    // The ask HOLDS it, and says so — the source line is how a person sees
    // what the ask is holding and can take it back (spec §2), and waiting on
    // it is waiting for the paste to have landed rather than for a duration.
    // A document has no name of its own, so the line says what it is.
    await expect(ask.locator('.api-import-source')).toContainText('Pasted Postman export')

    // ── AND WHERE IT GOES, through the pencil ─────────────────────────────
    //
    // The destination is an offer rendered as a sentence; the field behind
    // it is still the truth and is still what a person types into once they
    // disagree with the offer. This spec disagrees: the collection must land
    // where the walk below can read it, not under the collections root.
    await ask.getByRole('button', { name: 'Change where this goes' }).click()
    await page.locator('#api-import-postman-dest').fill(collectionRoot)
    await ask.getByRole('button', { name: 'Import', exact: true }).click()

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
    // The importer leaves credential-shaped variables empty; the person
    // supplies the value through the shared secret picker below.
    const env = readFileSync(join(collectionRoot, 'environments', 'default.json')).toString('utf8')
    const parsed: unknown = JSON.parse(env)
    if (typeof parsed !== 'object' || parsed === null || !('values' in parsed)) {
      throw new Error('imported environment has no values object')
    }
    const values = parsed.values
    if (typeof values !== 'object' || values === null || Array.isArray(values)) {
      throw new Error('imported environment values is not an object')
    }
    expect('token' in values).toBe(false)

    // ── Send, and NOTHING IS PRESSED TO GET HERE ──────────────────────────
    //
    // The import OPENS what it wrote (nocx-vkp9d). This spec used to go to
    // the panel's other ask afterwards and type the path that had been in the
    // field in front of it a second earlier, and that second open is now a
    // SECOND handle on the same folder: the tree drew the collection twice
    // and every row in it matched two elements, which is a strict-mode
    // violation rather than a subtle wrong. What arrives below with nothing
    // pressed is the difference between a directory on disk and a collection
    // somebody can use.
    const requestRow = workbench
      .locator('.api-tree__row')
      .filter({ hasText: TELEGRAM_REQUEST_NAME })
    await expect(requestRow).toBeVisible({ timeout: 10_000 })
    await requestRow.click()

    // The form shows the file, UNRESOLVED: the file is the truth and the
    // form is a projection of it (§6.4). This is the address a person sees,
    // and the token is not in it.
    await expect(workbench.locator('#api-url')).toHaveValue('{{baseUrl}}/bot{{token}}/sendMessage')
    const urlField = workbench.locator('#api-url')
    const picker = page.getByRole('listbox', { name: 'vault secrets' })
    const placeCaretBeforePath = async (): Promise<void> => {
      await urlField.evaluate((el) => {
        const input = el as HTMLInputElement
        const pos = input.value.indexOf('/sendMessage')
        if (pos < 0) throw new Error('the test URL has no /sendMessage suffix')
        input.focus()
        input.setSelectionRange(pos, pos)
      })
    }

    // `@token` is inside a word in this URL path, so the passive picker
    // deliberately does not open. The request panel's explicit door is the
    // product path for this case.
    await urlField.fill('{{baseUrl}}/bot@token/sendMessage')
    await expect(picker).not.toBeVisible()

    // Create the token through the same explicit door, but start at a
    // whitespace boundary so the picker can accept the requested name.
    await urlField.fill('{{baseUrl}}/bot ')
    await workbench.getByRole('button', { name: 'More actions for this request' }).click()
    const requestMenu = page.getByTestId('api-request-row-menu')
    await requestMenu.getByRole('menuitem', { name: 'Insert a secret…' }).click()
    await urlField.pressSequentially('token')
    await expect(picker).toBeVisible()
    await expect(picker.getByRole('option', { name: /Add "token"/ })).toBeVisible()
    await urlField.press('Enter')

    const addSecret = page.getByRole('dialog').filter({ hasText: 'Add secret' })
    await expect(addSecret).toBeVisible()
    await addSecret.locator('#sr-add-name').fill('token')
    await addSecret.locator('#sr-add-value').fill(TELEGRAM_BOT_TOKEN)
    await addSecret.getByRole('button', { name: 'Add secret', exact: true }).click()
    await expect(addSecret).not.toBeVisible()

    // Creating a record hands the surface to Settings. Return to the
    // workbench, use the explicit door at the middle-word caret, and accept
    // the real row.
    await page.locator('.activity-bar button[data-action="api"]').click()
    await expect(workbench).toBeVisible()
    await urlField.fill('{{baseUrl}}/bot/sendMessage')
    await placeCaretBeforePath()
    await workbench.getByRole('button', { name: 'More actions for this request' }).click()
    await requestMenu.getByRole('menuitem', { name: 'Insert a secret…' }).click()
    await expect(picker).toBeVisible()
    await expect(picker.getByRole('option', { name: 'token', exact: true })).toBeVisible()
    await urlField.press('Enter')
    await expect(urlField).toHaveValue(/\{\{secret:[^}]+\}\}/)

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()

    const run = workbench.locator('.api-run').first()
    await expect(run).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    const savedFiles = walk(collectionRoot)
    for (const file of savedFiles) {
      const text = readFileSync(file, 'utf8')
      expect(text, `${file} carries the token`).not.toContain(TELEGRAM_BOT_TOKEN)
      expect(text, `${file} carries the secret name`).not.toContain('"token"')
    }
    const requestFile = savedFiles.find((file) =>
      readFileSync(file, 'utf8').includes(`"name": "${TELEGRAM_REQUEST_NAME}"`),
    )
    if (requestFile === undefined) throw new Error('imported request file was not found')
    expect(readFileSync(requestFile, 'utf8')).toContain('{{secret:')

    // ── 1. THE SERVER RECEIVED THE REAL VALUE ─────────────────────────────
    //
    // 200 is the credential check: the route is registered under the real
    // token's path and everything else is a 404, so this number cannot be
    // reached by a request carrying `{{token}}`.
    await expect(run.locator('.api-run__stats')).toContainText('HTTP status 200')
    // The body is LAID OUT FOR READING (nocx-7c39h, nocx-dhojo), so this
    // surface indents the document and CodeMirror draws a line-number gutter
    // beside it. The claim being made is that the bytes the server sent are
    // what is on screen, so it is made against the editor's own content —
    // never the gutter — with the whitespace the layout added taken back
    // out. Raw, asserted below, is the surface that keeps them exactly.
    const shownBody = run.locator('[aria-label="Response body"] .cm-content')
    await expect(shownBody).toBeVisible()
    expect((await shownBody.innerText()).replace(/\s/g, '')).toContain(
      SENT_MESSAGE_BODY.replace(/\s/g, ''),
    )
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
