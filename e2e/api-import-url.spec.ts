/**
 * e2e: an export arrives BY URL, and the collection keeps the route it
 * arrived through — the epic's happy path, watched (Task 8 of the import-ask
 * plan).
 *
 * The sentence this file is: a person pastes the address of a Postman export,
 * presses Import, and gets a collection in the tree whose environment already
 * routes the way the fetch did. Before this the ask could be given an address
 * and had nowhere to put it; a document reachable only from inside a network
 * is exactly the document nobody can drop, because it never reaches their
 * machine at all (spec §1).
 *
 * THE FETCH IS REAL AND LOCAL. The export is served by an HTTP server this
 * spec starts on 127.0.0.1 and an ephemeral port, and the thing that GETs it
 * is the DEVHARNESS — a separate process from Playwright's worker, which is
 * the whole point: `apifetch` runs in the backend, so a fetch that never left
 * the renderer would not appear in this server's log at all. The server owns
 * the port the same way every other fixture here does, by binding 0 and
 * reading back what the OS gave (nocx-z9s9.11 is the run where six specs
 * fought over six hand-picked numbers).
 *
 * ## What this cannot watch, and it is a gap rather than an omission
 *
 * THE CONNECTION CASE. `route: {kind: "connection"}` makes the backend lease
 * the named profile's pooled SSH connection and open a **direct-tcpip**
 * channel through it (apisend/routes.go, apisend/ssh_dialer.go: the dial is
 * `lease.Dial(addr)`). The only SSH server this suite can start is
 * `cmd/e2e-sshd`, and it answers every channel type but `session` with
 * `UnknownChannelType` (cmd/e2e-sshd/main.go:237) — it authenticates, runs a
 * shell on a PTY and serves SFTP, and forwards no TCP at all. So a routed
 * fetch cannot succeed here for a reason that has nothing to do with the
 * feature, and a spec that drove it would be watching the fixture refuse
 * rather than the product work.
 *
 * The routed half is therefore covered where it can be: `internal/apifetch`
 * proves the route table is consulted and that a fetch refuses rather than
 * dialling around a bastion it cannot lease (nocx-7fopj), `internal/apiimport`
 * proves a connection route reaches the minted environment, and the renderer's
 * own tests prove the picker sends it. Nothing joins those ends. Closing it
 * means teaching `cmd/e2e-sshd` to forward a direct-tcpip channel, which is
 * a change outside `e2e/` — reported to the owner so it becomes a bead, since
 * a gap that lives only in a comment evaporates.
 *
 * ITS OWN BACKEND, like api-import.spec.ts and for the same two reasons: the
 * export carries a bearer token so this run needs a vault it set up itself
 * rather than leaving one behind for every other spec, and the destination the
 * ask proposes is under the collections folder of the isolated home THIS
 * backend resolved.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state: a
 * dialog on screen, a control appearing, a value in a field, a directory on
 * disk, a row in the tree, a request in the server's own log.
 */
import { test as base, expect } from '@playwright/test'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
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
const VAULT_PASSPHRASE = 'api-import-url-e2e-master-pass'

/**
 * The path the export is served at.
 *
 * Its LAST SEGMENT is load-bearing: a pasted URL proposes
 * `<defaultRoot>/<the last segment without any of its suffixes>`
 * (proposedDestinationFromURL), so this name is what makes the proposal
 * `…/collections/acme-api` — and it is the collection's own name, which is
 * what puts the same string in the tree row below.
 */
const EXPORT_PATH = `/exports/${POSTMAN_COLLECTION_NAME}.postman_collection.json`

/** The base URL the export declares. Nothing is ever sent to it — this spec
 *  imports and never presses Send — so it only has to be a URL the import can
 *  carry into the environment it writes. */
const UNREACHED_BASE_URL = 'http://127.0.0.1:9'

/** The export server: the address the ask is given, and the log that says the
 *  BACKEND is what came and got it. */
interface ExportServer {
  /** `http://127.0.0.1:<port>` — the origin the export is served from. */
  readonly origin: string
  /** `<method> <path>` per request received, in arrival order. */
  hits(): readonly string[]
  stop(): Promise<void>
}

async function startExportServer(document: string): Promise<ExportServer> {
  const received: string[] = []
  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    received.push(`${req.method ?? ''} ${req.url ?? ''}`)
    if (req.url !== EXPORT_PATH) {
      res.writeHead(404).end('no such export')
      return
    }
    // `application/json` because that is what a server serving an export
    // sends — NOT because the import consults it. apifetch decides what it
    // fetched by the first non-space byte and never by Content-Type
    // (fetch.go), so this header is realism and not a fixture that props the
    // product up.
    res.writeHead(200, { 'content-type': 'application/json' }).end(document)
  })
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })
  const port = (server.address() as AddressInfo).port
  return {
    origin: `http://127.0.0.1:${port}`,
    hits: () => received,
    stop: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections()
        server.close(() => resolve())
      }),
  }
}

/** The route an environment file declares — the half of apicoll.Environment
 *  this spec reads (apicoll/collection.go). */
interface EnvironmentRoute {
  route?: { kind?: string; profileId?: string }
}

test.describe('an export arrives by URL', () => {
  // Three columns — tree, request, runs — must all be on screen for a person
  // to do this. The container's default viewport is narrower than the app's.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  let exportServer: ExportServer

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-import-url-')) }
    exportServer = await startExportServer(postmanExport(UNREACHED_BASE_URL))
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterAll(async () => {
    backend?.stop()
    await exportServer?.stop()
  })

  test('a pasted URL is fetched by the backend, and the collection keeps the route it arrived through', async ({
    page,
  }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // ── The vault first, because the export carries a bearer token ────────
    //
    // The import puts the VALUE in the vault and leaves the NAME in the
    // environment file (design §8.1), so there has to be a vault for it to
    // reach. This backend has no OS keystore, so it is the passphrase one and
    // a person sets it up from Settings → Secrets — through the product
    // rather than seeded around it.
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

    // ── The workbench, and the ask off the collections menu ───────────────
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })
    // The built-in collection is the first row a fresh stand has, and its
    // arrival is the listing's own account of having answered. Waiting on it
    // is what makes `defaultRoot` known by the time the ask opens, so a
    // failure here reads as "the listing never came back" rather than as a
    // proposal bug.
    await expect(workbench.locator('.api-tree__row').first()).toBeVisible({ timeout: 15_000 })

    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Import collection…' }).click()
    const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })
    await expect(ask).toBeVisible()

    // ── The address, into the ask's one question ──────────────────────────
    //
    // The same box the export's TEXT goes into: what a paste IS is decided
    // once, in `classifyPastedSource`, and a URL is what starts `http://` or
    // `https://` (spec §2).
    const exportURL = `${exportServer.origin}${EXPORT_PATH}`
    await page.locator('#api-import-paste').fill(exportURL)

    // THE ROUTE CONTROL IS WHAT SAYS THE ASK RECOGNISED IT. Only a URL is
    // fetched, so only a URL has a route, and the picker appears with the URL
    // and goes again with it (import-dialogs.tsx). Its arrival is the ask's
    // own account of holding an address rather than a document — an
    // observable state, and the one this step is about.
    await expect(page.locator('#api-import-route')).toBeVisible()
    // Direct is the resting state and this stand has no connections to offer,
    // so the picker sits on its placeholder. That is also the route the
    // assertion at the end expects the collection to have inherited.
    await expect(page.locator('#api-import-route')).toHaveValue('')

    // ── …and the URL proposes where it goes ───────────────────────────────
    //
    // THROUGH THE PENCIL, because the destination is a sentence until
    // somebody disagrees with it. Nothing is typed into the field: this spec
    // takes the proposal, and reads it back out because the field is the
    // truth (api-paths.ts — an offer, not a derivation) and rebuilding the
    // path here would make this spec a second owner of it.
    await ask.getByRole('button', { name: 'Change where this goes' }).click()
    const dest = page.locator('#api-import-postman-dest')
    await expect(dest).toHaveValue(
      new RegExp(`[\\\\/]collections[\\\\/]${POSTMAN_COLLECTION_NAME}$`),
    )
    const collectionRoot = await dest.inputValue()
    expect(existsSync(collectionRoot), 'the destination existed before the import').toBe(false)

    const importButton = ask.getByRole('button', { name: 'Import', exact: true })
    await expect(importButton).toBeEnabled()
    await importButton.click()

    // ── The document arrives, and so does the folder ──────────────────────
    //
    // THE FOLDER ARRIVING is the import's closing event (design §12.2: dest
    // does not exist from before the first byte until the rename has landed),
    // so waiting on it is waiting on a state rather than on a duration.
    await expect
      .poll(() => existsSync(collectionRoot), {
        message: `the import never produced ${collectionRoot}: ${backend.logTail()}`,
        timeout: 20_000,
      })
      .toBe(true)
    // And the ask closes itself on success — a dialog still on screen over a
    // folder that arrived is the surface disagreeing with the disk.
    await expect(ask).toBeHidden()

    // ── The collection is in the tree, with nothing else pressed ──────────
    //
    // The import OPENS what it wrote (nocx-vkp9d), so what arrives here is
    // the difference between a directory on disk and a collection somebody
    // can use.
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_COLLECTION_NAME }),
    ).toBeVisible({ timeout: 10_000 })
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_REQUEST_NAME }),
    ).toBeVisible({ timeout: 10_000 })

    // ── THE FETCH WAS REAL, AND THE BACKEND IS WHAT MADE IT ───────────────
    //
    // One GET, for that path. This server is in the Playwright worker and the
    // fetch is in the devharness the worker spawned, so a hit here can only
    // have come across a socket — an import that had somehow read the
    // document any other way would leave this list empty.
    expect(exportServer.hits()).toEqual([`GET ${EXPORT_PATH}`])

    // ── AND THE COLLECTION KEEPS THE ROUTE IT ARRIVED THROUGH ─────────────
    //
    // This is the half that makes the feature worth having (spec §6): a
    // collection fetched through a connection whose environment says `direct`
    // is a collection where every request fails until the person sets by hand
    // the thing they had already told the import. Fetched DIRECTLY, as here,
    // the same rule says `direct` — and asserting it on the direct case is
    // what makes the field a value the import decided rather than a constant
    // it always writes.
    const env = JSON.parse(
      readFileSync(join(collectionRoot, 'environments', 'default.json'), 'utf8'),
    ) as EnvironmentRoute
    expect(env.route?.kind, 'the imported environment does not carry a route').toBe('direct')
    // …and it names no profile, because no connection was chosen. A profile
    // left on a direct route would send every request under this collection
    // through a connection the person never picked.
    expect(env.route?.profileId ?? '').toBe('')
  })
})
