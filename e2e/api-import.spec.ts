/**
 * e2e: the import ask on a stand with no Wails — which is every stand this
 * repository can run (bead nocx-twmaf, Task 7 of the import-ask plan).
 *
 * ## What this file can watch, and what nothing here can
 *
 * The gesture the feature was built for is the NATIVE WINDOW DROP: a person
 * drags a Postman export from Finder onto the ask, and Wails hands Go the
 * file's absolute path. No Playwright gesture can produce it. The headless
 * stand is `cmd/devharness` plus vite and has no Wails in it at all, the
 * native drop never becomes a DOM event, and `SourceTicketStore.Dropped` is
 * deliberately unreachable over JSON-RPC — the wire may never mint a source.
 * `e2e/drop-gesture.ts`, which the terminal's specs use, builds a
 * `DataTransfer` and dispatches `DragEvent`s: that is a BROWSER drop and a
 * different mechanism, and using it here would be a check that watches
 * something the product does not do.
 *
 * So the native half is covered by three things meeting at
 * `contracts/files.dropped.schema.json` — a Go test on `Dropped`, the
 * over-the-wire conformance test, and the renderer's own unit tests — and
 * nothing joins the two ends. That gap is filed rather than implied;
 * `nocx-9le.5.23` is the precedent, where the local-tab drop had no
 * end-to-end check and was the half that broke twice.
 *
 * ## What it does watch, and why that is not a consolation prize
 *
 * A stand with no Wails is not a degraded case here: it is the state every
 * contributor develops in, and `make dev-web` ships to a browser. So the ask
 * must be fully usable with nothing but a keyboard, and it must NOT advertise
 * a drop surface it cannot honour — a zone that highlights under a drag and
 * then delivers nothing has already promised. Absence is the capability, the
 * same rule the ask's two system pickers follow.
 *
 * ITS OWN BACKEND, like `api-testing.spec.ts` and `api-secret-in-path.spec.ts`
 * and for the same two reasons. The import writes a secret VALUE, so this run
 * needs a vault it set up itself rather than leaving one behind for every
 * other spec; and the destination the ask proposes is under the collections
 * folder of the isolated home THIS backend resolved, so it is only knowable
 * for a backend this spec owns. On the shared stand the same import would put
 * a folder in the home every other spec reads.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state: a
 * row in the tree, a dialog on screen, a value in a field, a directory on
 * disk.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { existsSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { bindEndpoint, settingsReady, VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'
import {
  POSTMAN_COLLECTION_NAME,
  POSTMAN_REQUEST_NAME,
  postmanExport,
} from './fixtures/postman-export'

const test = base

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const devharnessBin = (): string => readStand().devharness

/** The vault passphrase this run sets up. It is a passphrase, not a token —
 *  nothing here asserts its absence and it never reaches a collection. */
const VAULT_PASSPHRASE = 'api-import-ask-e2e-master-pass'

/** The export's file NAME is load-bearing: the destination the ask proposes
 *  is `<defaultRoot>/<the name without any of its extensions>`, so this name
 *  is what makes the proposal `…/collections/acme` (api-paths.ts). */
const EXPORT_FILE = 'acme.postman_collection.json'

/** The base URL the export declares. Nothing is sent in this spec — no test
 *  server is started — so it only has to be a URL the import can carry into
 *  the environment it writes. */
const UNREACHED_BASE_URL = 'http://127.0.0.1:9'

test.describe('the import ask on a stand with no Wails', () => {
  // Three columns — tree, request, runs — must all be on screen for a person
  // to do this. The container's default viewport is narrower than the app's.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  /** The export on disk, as a person would have it after clicking Export in
   *  Postman. Outside the backend's isolated home on purpose: a document
   *  somebody downloaded is not in the app's own directory, and the ask has
   *  to accept a path from anywhere. */
  let exportPath: string

  // A BACKEND PER TEST, not per file. Both tests set the vault up through
  // Settings, and a vault is a once-only walk: the second test on a shared
  // home would find protection already on and the button it presses gone.
  // Fresh homes also keep the collections folder empty, which is what makes
  // "the destination does not exist yet" a fact rather than a hope.
  test.beforeEach(() => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-api-import-')) }
    exportPath = join(disposable.root, EXPORT_FILE)
    writeFileSync(exportPath, postmanExport(UNREACHED_BASE_URL), 'utf8')
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  /** Reach the workbench with the ask open, and answer with the two locators
   *  both tests then read. The waits inside are the preconditions of the ask
   *  rather than assertions about it: the tree having a row is the listing
   *  having landed, and the listing is what carries `defaultRoot` — the value
   *  the ask proposes from. */
  const openTheAsk = async (page: Page) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // The vault first, because the export carries a bearer token and the
    // import has to have somewhere to put it (design §8.1). This backend has
    // no OS keystore, so the vault is the passphrase one and a person sets it
    // up from Settings → Secrets — the same walk api-secret-in-path.spec.ts
    // takes, through the product rather than seeded around it.
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

    // The activity-bar entry, which is how a person reaches the workbench.
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })
    // The built-in collection is the first row a fresh stand has, and its
    // arrival is the listing's own account of having answered. Waiting on it
    // is what makes `defaultRoot` known by the time the ask opens — asserted
    // separately from the ask so a failure here reads as "the listing never
    // came back" rather than as a prefill bug.
    await expect(workbench.locator('.api-tree__row').first()).toBeVisible({ timeout: 15_000 })

    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Import collection…' }).click()
    const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })
    await expect(ask).toBeVisible()
    return { workbench, ask }
  }

  test('the ask opens on our collections folder and imports a typed path', async ({ page }) => {
    const { workbench, ask } = await openTheAsk(page)

    // ── The ask opens already holding OUR folder ──────────────────────────
    //
    // Matched by shape rather than against a second derivation of the path:
    // `<DataDir>/collections` is resolved by internal/storage from the
    // isolated home, and a spec that recomputed it would be a second owner of
    // that answer — the defect e2e/harness.ts's documentDir comment records
    // being paid for twice already. What matters here is that the field
    // opens on the collections root, and that it opens with the trailing
    // separator, because that is the proposal the next assertion completes.
    const dest = page.locator('#api-import-postman-dest')
    await expect(dest).toHaveValue(/[\\/]collections[\\/]$/)

    // …and Import is REFUSED on it. The root certainly exists, so an import
    // of it could only come back "a folder is already there" — about a folder
    // the person never chose. A prefill that could be submitted would have
    // bought a round trip to learn what the form already knew.
    const importButton = ask.getByRole('button', { name: 'Import', exact: true })
    await expect(importButton).toBeDisabled()

    // ── Naming the export completes the proposal ──────────────────────────
    //
    // Typed, not dropped and not picked: this is the path a person takes on
    // any stand without Wails, and it is the only one this harness has.
    await page.locator('#api-import-postman-file').fill(exportPath)
    await expect(dest).toHaveValue(/[\\/]collections[\\/]acme$/)
    await expect(importButton).toBeEnabled()

    // The field is the truth (api-paths.ts: an offer, not a derivation), so
    // the disk assertion below reads the destination OUT of it rather than
    // rebuilding it.
    const collectionRoot = await dest.inputValue()
    expect(existsSync(collectionRoot), 'the destination existed before the import').toBe(false)

    await importButton.click()

    // THE FOLDER ARRIVING is the import's closing event (design §12.2: dest
    // does not exist from before the first byte until the rename has landed),
    // so waiting on it is waiting on a state rather than on a duration.
    await expect
      .poll(() => existsSync(collectionRoot), {
        message: `the import never produced ${collectionRoot}`,
        timeout: 15_000,
      })
      .toBe(true)
    // And the ask closes itself on success — a dialog still on screen over a
    // folder that arrived is the surface disagreeing with the disk.
    await expect(ask).toBeHidden()

    // ── …and the collection is in the tree, with nothing else pressed ─────
    //
    // The import OPENS what it wrote (nocx-vkp9d). It used not to —
    // `api.collections.list` answers the open folders and the import
    // registers nothing — so this spec went to "Open a collection folder…"
    // afterwards and typed the path that had been in the field beside it a
    // moment earlier. That second step was the defect, not the procedure, so
    // nothing is pressed between the Import above and the rows below: what
    // arrives is the difference between a directory on disk and a collection
    // somebody can use.
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_COLLECTION_NAME }),
    ).toBeVisible({ timeout: 10_000 })
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_REQUEST_NAME }),
    ).toBeVisible({ timeout: 10_000 })
  })

  test('no Wails runtime means no drop target, not a dead one', async ({ page }) => {
    const { ask } = await openTheAsk(page)

    // The ask is really here and really usable — otherwise the absence below
    // would pass on a dialog that never opened, which is the way an
    // absence assertion usually lies.
    await expect(page.locator('#api-import-postman-file')).toBeVisible()

    // The zone IS rendered: the ask wears the kit's DropZone whatever the
    // build, and what the build decides is whether it names itself a target.
    const zone = ask.locator('.ui-drop-zone')
    await expect(zone).toHaveCount(1)

    // And it draws NOTHING: the region — the icon, the line naming the
    // gesture and its picker button — is the affordance, and here it is
    // absent rather than merely inert. A region inviting a drop that cannot
    // arrive has already promised (nocx-9hb5g).
    await expect(zone.locator('.ui-drop-zone__region')).toHaveCount(0)

    // And it names nothing. `data-file-drop-target` is the attribute the
    // backend reads off the dropped-on element, `data-session-id` is the tab
    // it would be attributed to, and here there is no Wails to deliver either
    // — so advertising them would be advertising a gesture that cannot
    // arrive. Asserted on the whole page rather than inside the ask, because
    // an api-import target anywhere is the same broken promise.
    await expect(page.locator('[data-file-drop-target="api-import"]')).toHaveCount(0)
    await expect(zone).not.toHaveAttribute('data-session-id', /.*/)
  })
})
