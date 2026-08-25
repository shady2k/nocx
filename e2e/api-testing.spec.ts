/**
 * e2e: the API workbench, end to end — the epic's DONE WHEN (bead nocx-m61um).
 *
 * Design §1 names the check this file is, in one sentence:
 *
 *   > A test server is started locally. A Postman v2.1 collection file
 *   > carrying `{{baseUrl}}/users` and a bearer token is imported. The test
 *   > asserts the token is in the vault and NOT in any file on disk; opens the
 *   > request; presses Send; finds a run with `201` and the decoded body;
 *   > opens raw; and asserts the token appears there as a badge naming the
 *   > secret, never as its bytes.
 *
 * WHY THIS FILE AND NOT MORE UNIT TESTS. AGENTS.md testing rule 2: `deadcode`
 * and coverage are floors and neither can report a feature that is MISSING —
 * only that written code is used. The connection manager shipped with no way
 * to create a group while 1041 frontend tests were green, every one of them
 * mounting a component and asserting what it rendered. So every step below
 * goes through the seam a person actually reaches: the activity-bar entry, the
 * collections menu, the import ask it opens, the region's file picker inside
 * it, the folder ask, a row in the tree, the Send button, the Raw segment.
 * Nothing here calls ApiClient, and nothing here calls the control plane to
 * arrange a state the product is supposed to arrange.
 *
 * ITS OWN BACKEND, not the shared stand. Two reasons and both are hard. The
 * import writes a secret VALUE, so this run needs a vault it set up itself —
 * and doing that on the stand every other spec shares would leave a vault
 * behind for them. And the walk in step 3 has to read the collection folder
 * and the binding document off the same isolated home the backend resolved,
 * which is only knowable for a backend this spec owns. `withoutSecretService`
 * is true so the passphrase path is the one taken in the container and on a
 * Mac alike (harness.ts states why the env var and the bus address are both
 * needed).
 *
 * NOTHING HERE WAITS ON A DURATION. There is no `waitForTimeout` in this file:
 * every wait is on an observable state — a dialog on screen, a file on disk, a
 * row in the tree, a run in the list. A spec that needs a slow machine to pass
 * is broken on a fast one too; it has only not been caught yet.
 */
import { test as base, expect } from '@playwright/test'
import { existsSync, mkdtempSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import {
  bindEndpoint,
  documentDir,
  settingsReady,
  VaultBackend,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'
import { startApiTestServer, type ApiTestServer } from './fixtures/api-test-server'
import {
  POSTMAN_BEARER_TOKEN,
  POSTMAN_COLLECTION_NAME,
  POSTMAN_REQUEST_BODY,
  POSTMAN_REQUEST_NAME,
  postmanExport,
} from './fixtures/postman-export'

const test = base

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = (): string => readStand().devharness

/** The vault passphrase this run sets up. It is a passphrase, not a token —
 *  nothing asserts its absence anywhere, and it never reaches a collection. */
const VAULT_PASSPHRASE = 'api-testing-e2e-master-pass'

/** The variable name the import mints for a bearer token it had to take out
 *  of the export (apiimport/secretvar.go: `namer.take("token")`). It is the
 *  name the badge in the raw view must show, and the name the environment
 *  file must declare — the file carries the NAME and never the value (§6.3). */
const SECRET_VAR = 'token'

/** Every file under `root`, recursively, as absolute paths — dotfiles
 *  included.
 *
 *  A WALK, not a look at one file, and design §8 is why: the claim is that a
 *  collection folder is safe to commit BY CONSTRUCTION, so the thing to check
 *  is the whole folder rather than the one file somebody thought of. A check
 *  of `collection.json` alone would have passed on a build that wrote the
 *  token into `environments/default.json`. */
function walk(root: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(root)) {
    const full = join(root, entry)
    if (statSync(full).isDirectory()) out.push(...walk(full))
    else out.push(full)
  }
  return out
}

test.describe('API testing: import, send, and the token that never lands in a file', () => {
  // The workbench is three columns — tree, request, runs — and all three have
  // to be on screen for a person to do this. The container's default viewport
  // is narrower than the shipped app's window.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  let server: ApiTestServer
  /** Where the import puts the collection. It must NOT exist beforehand — an
   *  import refuses an occupied destination rather than replacing it (§12.2)
   *  — so it is a name under the disposable root and nothing creates it. */
  let collectionRoot: string

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-api-')) }
    server = await startApiTestServer({ expectedToken: POSTMAN_BEARER_TOKEN })
    collectionRoot = join(disposable.root, 'acme-api')
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterAll(async () => {
    backend?.stop()
    await server?.stop()
  })

  test('a Postman export becomes a collection with no credential in it; Send answers 201 and the raw view names the token instead of showing it', async ({
    page,
  }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // ── The vault first, because the import has somewhere to put the token ──
    //
    // Design §8.1: the VALUE lives in the vault, as every other secret in this
    // product does. This backend has no OS keystore, so the vault is the
    // passphrase one and a person sets it up from Settings → Secrets. Doing it
    // here rather than seeding it makes the precondition a thing the product
    // can actually reach.
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

    // ── Step 1 of the scenario: the workbench a person opens ────────────────
    //
    // The activity-bar entry, which design §9.2 says opens or focuses the pane
    // and never expands the side panel. Reaching the pane any other way would
    // prove the pane works and say nothing about whether it can be got to.
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })

    // ── Step 2: the export is imported THROUGH THE UI ───────────────────────
    //
    // The import is an ASK off the collections menu, and it was a collapsed
    // section in the panel when this spec was written: a panel that wears a
    // form is a panel asking you to fill something in before it will show you
    // anything (import-dialogs.tsx). The menu and the dialog it opens are the
    // seam a person reaches now, so they are the seam this walks.
    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Import collection…' }).click()
    const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })
    await expect(ask).toBeVisible()

    // THE EXPORT, AS TEXT. The ask no longer names two absolute paths
    // (nocx-ysyy2), and on a stand with no Wails neither gesture that ANSWERS
    // with a path exists — no system picker, no native window drop. What is
    // left is the ask's one question, and the export's own text is a source it
    // takes: bytes reach the backend wherever it runs, where a path only names
    // a file on the machine running Go (spec §1a).
    //
    // THE CREDENTIAL ASSERTION IN STEP 5 IS UNAFFECTED, and it is worth saying
    // why rather than hoping. Every entrance this build has hands the renderer
    // the BYTES — a chosen file is read with `File.text()` and sent as a
    // document too (api-pane.tsx) — and what step 5 watches is the SERIALISED
    // DOM, where the box holds what was pasted as a property rather than as
    // markup. It says what it always said: nothing nocx renders carries the
    // value.
    await page.locator('#api-import-paste').fill(postmanExport(server.baseUrl))
    // The ask HOLDS it and says so: the source line is what a person reads to
    // see what the ask is holding (spec §2), and waiting on it waits for the
    // paste to have landed rather than for a duration. A pasted document has
    // no name of its own, so the line says what it is.
    await expect(ask.locator('.api-import-source')).toContainText('Pasted Postman export')

    // AND WHERE IT GOES, through the pencil. The destination is an offer
    // rendered as a sentence; the field behind it is still the truth and is
    // what a person types into once they disagree — and this spec disagrees,
    // because the walk in step 3 reads a folder it named itself.
    await ask.getByRole('button', { name: 'Change where this goes' }).click()
    await page.locator('#api-import-postman-dest').fill(collectionRoot)
    await ask.getByRole('button', { name: 'Import', exact: true }).click()

    // THE FOLDER ARRIVING is the observable state, and §12.2 makes it the
    // exactly right one: "dest DOES NOT EXIST from before the first byte is
    // written until the rename has landed AND every binding this import
    // declares has been written … any failure after the rename removes it
    // again." So the directory existing is the import's closing event, and
    // waiting on it is waiting on a state rather than on a duration.
    //
    // The DIRECTORY, deliberately, and not a file inside it. Which files an
    // import writes is apiimport's to decide and apicoll's to read; a spec
    // naming one of them would be a third party to that agreement, and would
    // go red for a rename that broke nothing.
    await expect
      .poll(() => existsSync(collectionRoot), {
        message: `the import never produced ${collectionRoot}`,
        timeout: 15_000,
      })
      .toBe(true)

    // ── Step 3: the walk — design §8, and the assertion the format exists for ─
    //
    // "A hostile file can write {{token}} and gets whatever the reader bound
    // in their own environment; it has no way to spell 'the password behind
    // the production SSH profile', because there is no syntax in which a file
    // names a secret." So two things must be absent from EVERY file under the
    // root: the credential itself, and any identifier for it.
    //
    // The identifier is not guessed at from a shape — it is read out of the
    // binding document, which is the one place in this feature that holds one
    // (apibind/store_impl.go). That also makes this the check that the token
    // reached the VAULT: a run where nothing was bound would have no ids to
    // look for, and the assertion below refuses to pass on an empty list.
    const bindingsPath = join(documentDir(backend.isolatedHome), 'api-bindings.json')
    // WAITED FOR, because the directory arriving is not this import's last
    // event — the comment above is right about what §12.2 promises and wrong
    // about who it promises it to. ImportInto renames the staging tree into
    // place and only THEN offers each secret value to the BindWriter (step 5
    // of write.go's own contract), and that offer is a vault write: a key
    // derivation and an encrypt against the passphrase vault step 0 set up,
    // before api-bindings.json is rewritten. So there is a window in which
    // dest exists and no binding document does. What §12.2 rules out is that
    // window ever becoming permanent — a failure after the rename calls undo
    // and takes dest away again — which is exactly why waiting here is safe
    // and reading once is not.
    //
    // The wait is on the state and never on a duration (nocx-2qo02): webkit
    // lost this race on a loaded runner at a commit that had been green an
    // hour before, and a sleep would only have moved the loss further out.
    // Polling the COUNT rather than the file also keeps the assertion the
    // next line depends on — a document holding no binding fails here rather
    // than one line later with a worse message.
    const boundSecretIds = (): string[] => {
      if (!existsSync(bindingsPath)) return []
      const doc = JSON.parse(readFileSync(bindingsPath, 'utf8')) as {
        bindings?: { collection: string; environment: string; variable: string; secretId: string }[]
      }
      return (doc.bindings ?? []).map((b) => b.secretId)
    }
    await expect
      .poll(() => boundSecretIds().length, {
        message: 'the import bound no secret at all — no binding document, or nothing in it',
        timeout: 15_000,
      })
      .toBeGreaterThan(0)
    const secretIds = boundSecretIds()

    // And the binding document is NOT inside the collection: §8.1's whole
    // point is that the identifier is held by the app, beside the vault.
    expect(bindingsPath.startsWith(collectionRoot)).toBe(false)

    const files = walk(collectionRoot)
    expect(files.length, 'the import wrote no files').toBeGreaterThan(0)
    for (const file of files) {
      const text = readFileSync(file).toString('utf8')
      expect(text, `${file} carries the credential from the export`).not.toContain(
        POSTMAN_BEARER_TOKEN,
      )
      for (const id of secretIds) {
        expect(text, `${file} names a vault secret (${id})`).not.toContain(id)
      }
    }

    // The absence above is only worth anything if the folder DECLARES the
    // variable — otherwise a build that dropped the credential on the floor
    // would pass every line of it. The environment names it and holds no
    // value for it (§6.3), and the request's auth names the same variable.
    const envText = readFileSync(join(collectionRoot, 'environments', 'default.json')).toString(
      'utf8',
    )
    expect(JSON.parse(envText) as { secretVars?: string[] }).toMatchObject({
      secretVars: [SECRET_VAR],
    })

    // ── Step 4: the request opens and Send is pressed ───────────────────────
    //
    // NOTHING IS PRESSED BETWEEN THE IMPORT AND THESE ROWS. The import OPENS
    // what it wrote (nocx-vkp9d); it used not to — `api.collections.list`
    // answers the open folders and the import registered nothing — so this
    // spec used to go to the panel's other ask afterwards and type the path
    // that had been in the field in front of it a second earlier. That second
    // step was the defect and not the procedure, and what arrives below is the
    // difference between a directory on disk and a collection somebody can
    // use.
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_COLLECTION_NAME }),
    ).toBeVisible({ timeout: 10_000 })
    const requestRow = workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_REQUEST_NAME })
    await expect(requestRow).toBeVisible()
    await requestRow.click()

    // The form is showing the file, unresolved — the file is the truth and the
    // form is a projection of it (§6.4), so `{{baseUrl}}` is what a person
    // sees here and the environment is what resolves it at send time.
    await expect(workbench.locator('#api-url')).toHaveValue('{{baseUrl}}/users')
    // The auth field is TEXT: the file carries the `{{token}}` reference,
    // exactly as it does in the URL above — one resolver, not two
    // (nocx-6hg2w.20).
    await expect(workbench.locator('#api-auth-var')).toHaveValue('{{' + SECRET_VAR + '}}')

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()

    const run = workbench.locator('.api-run').first()
    await expect(run).toBeVisible({ timeout: 20_000 })
    // 201, and the number is the credential check as much as the route one:
    // the test server answers 401 for a request that arrives without exactly
    // the token the export declared (fixtures/api-test-server.ts).
    // The status through the kit's own account of it: StatusDot puts the
    // meaning in a visually-hidden span beside a decorative dot, so
    // "HTTP status 201" is the text a screen reader and this spec both read.
    await expect(run.locator('.api-run__stats')).toContainText('HTTP status 201')
    // …and the DECODED body, as a body rather than as base64 (§12.3).
    //
    // FIELD BY FIELD, not byte for byte. The Body tab lays a JSON answer out
    // for reading (nocx-dhojo) — one field per line, indented — so a minified
    // string is no longer what it renders, and its text carries the gutter's
    // line numbers besides. Raw is where the bytes as they arrived are
    // asserted, three lines below; these two tabs answer two questions and
    // this is the one that asks "can a person read the answer".
    const responseBody = run.locator('[aria-label="Response body"]')
    await expect(responseBody).toContainText('"id": "usr_8f21"')
    await expect(responseBody).toContainText('"email": "a@b.c"')

    // ── Step 5: raw, where the token is a badge and never its bytes ─────────
    // A TAB, not a radio: the three parts of an exchange were a segmented
    // control of two and are a tab row of three (run-list.tsx).
    await run.getByRole('tab', { name: 'Raw' }).click()
    const raw = run.locator('.api-run__raw')
    await expect(raw).toBeVisible()
    // The badge NAMES the secret — design §11.1's first state, where the badge
    // is evidence rather than a curtain.
    await expect(raw.locator('.ui-secret-chip__name').first()).toHaveText(SECRET_VAR)
    // The request line and the body are really there, so "no token in the DOM"
    // is not passing because the raw view is empty.
    await expect(raw.locator('[aria-label="Raw request"]')).toContainText('POST /users')
    await expect(raw.locator('[aria-label="Raw request"]')).toContainText(POSTMAN_REQUEST_BODY)

    // THE BYTES ARE ABSENT, asserted on the serialised page rather than on the
    // badge being present: a chip beside a leak is still a leak, and the value
    // could ride in an attribute — a title, a data-*, a hidden node — that no
    // text assertion would ever look at. `page.content()` is the whole
    // document as markup, which is the only place that covers all of them.
    expect(await page.content(), 'the credential reached the renderer in the clear').not.toContain(
      POSTMAN_BEARER_TOKEN,
    )

    // And the far side saw the real credential — which is what makes the
    // elision above an ELISION rather than a request that never carried one.
    const hits = server.requests().filter((r) => r.path === '/users')
    expect(hits).toHaveLength(1)
    expect(hits[0].authorization).toBe(`Bearer ${POSTMAN_BEARER_TOKEN}`)
    expect(hits[0].body).toBe(POSTMAN_REQUEST_BODY)

    // ── Step 5b: a literal pasted into the Auth tab is sent as the literal ─
    //
    // nocx-6hg2w.20's acceptance through the REAL wire: the same form, the
    // auth field's token text replaced with a raw literal, Send, and the
    // test server — which answers 401 for any Authorization that is not the
    // exact bound token — receives the literal as the header. That both
    // SENT it and did not rewrite it is the server's own account, asserted
    // after the row appears rather than on any timing.
    const pasted = '88730fee-9a4c-4c9d-8f4c-a1b2c3d4e5f6'
    // The field is on the request editor's AUTH TAB, which this walk has
    // not opened. The four sections of the editor are Tabs and the
    // inactive panels stay in the DOM yet are `hidden` (ui/tabs.tsx), so
    // the {{token}} assertion above could READ the value while an edit
    // cannot reach it — opening the tab is the gesture a person makes
    // before pasting here.
    await workbench.locator('.api-request').getByRole('tab', { name: 'Auth' }).click()
    await expect(workbench.locator('#api-auth-var')).toBeVisible()
    await workbench.locator('#api-auth-var').fill(pasted)
    await expect(workbench.locator('#api-auth-var')).toHaveValue(pasted)

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    const literalRun = workbench.locator('.api-run').first()
    await expect(literalRun).toBeVisible({ timeout: 20_000 })
    await expect(literalRun.locator('.api-run__stats')).toContainText('HTTP status 401')

    // The second request reached the wire and carried the LITERAL, not the
    // bound token and not a rewritten reference — the server's own record,
    // matched by its Authorization header.
    const literalHits = server.requests().filter((r) => r.authorization === `Bearer ${pasted}`)
    expect(literalHits).toHaveLength(1)
    expect(literalHits[0].path).toBe('/users')

    // And the raw view of THE LITERAL RUN shows the pasted value, unelided:
    // a literal is shown, which is the other half of the elision pair.
    await literalRun.getByRole('tab', { name: 'Raw' }).click()
    await expect(literalRun.locator('[aria-label="Raw request"]')).toContainText(pasted)

    // ── Step 6: a long body line moves the EDITOR, never the surface ────────
    //
    // Last, because it types into the body and this spec's own assertions are
    // done by here. It is in this file rather than in a unit test because
    // jsdom computes no layout: vitest and tsc were both green while a long
    // line pushed the whole request column sideways and drew a scrollbar under
    // the surface (nocx-kdawd). The defect was not the editor's — its scroller
    // was ready — but the tab bar's, whose mode select took its intrinsic
    // width and hung past the bar; the overflow then travelled up to the one
    // ancestor that scrolls.
    //
    // Asserted as an interval rather than a moment: nothing between the editor
    // and the pane may be wider than its own box, and the editor's scroller
    // must hold the line instead.
    await page.getByRole('tab', { name: /Body/ }).first().click()
    const editor = page.locator('.api-body-editor')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type(`{"t":"${'x'.repeat(400)}"}`)

    const geometry = await page.evaluate(() => {
      const names = [
        '.api-body-editor',
        '.ui-tabs__panel',
        '.ui-tabs',
        '.api-request',
        '.api-workbench__request',
      ]
      const wide = names.filter((sel) => {
        const el = document.querySelector(sel) as HTMLElement | null
        return el !== null && el.scrollWidth > el.clientWidth + 1
      })
      const sc = document.querySelector('.api-body-editor .cm-scroller') as HTMLElement | null
      return { wide, scrollerHoldsTheLine: sc !== null && sc.scrollWidth > sc.clientWidth }
    })
    expect(geometry.wide, 'these elements are wider than their own box').toEqual([])
    expect(geometry.scrollerHoldsTheLine, "the editor's own scroller takes the long line").toBe(
      true,
    )
  })
})
