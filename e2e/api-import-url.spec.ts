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
 * ## Both routes, and the second is the one the feature exists for
 *
 * DIRECT is the first case: the fetch goes out of this machine's own
 * interface and the minted environment says `direct`.
 *
 * THROUGH A CONNECTION is the second. `route: {kind: "connection"}` makes the
 * backend lease the named profile's pooled SSH connection and open a
 * **direct-tcpip** channel through it (apisend/routes.go, apisend/ssh_dialer.go:
 * the dial is `lease.Dial(addr)`), and the SSH server it leases is
 * `cmd/e2e-sshd` — which forwards that channel to the address it names since
 * nocx-n4rep. It used to answer every channel type but `session` with
 * `UnknownChannelType`, so a routed fetch failed here for a reason that had
 * nothing to do with the feature and this half could not be watched at all.
 *
 * THERE IS NO FALLBACK, and that is what makes the second case an assertion
 * rather than a hope: a connection route that cannot lease its profile
 * REFUSES (apisend's ErrNoConnection) instead of dialling around the bastion,
 * so an import that lands at all through a connection route is an import
 * whose bytes crossed the tunnel. The fixture's own `TCPIP=` line says the
 * same thing from the far side, and it is needed because both endpoints here
 * sit on this machine's loopback: at the destination a routed request and a
 * direct one are indistinguishable.
 *
 * ITS OWN BACKEND PER CASE, like api-import.spec.ts and for the same two
 * reasons: the export carries a bearer token so each run needs a vault it set
 * up itself rather than leaving one behind for every other spec, and the
 * destination the ask proposes is under the collections folder of the isolated
 * home THAT backend resolved. Per case rather than per file so neither case
 * inherits the other's vault, connections or collections — a spec that only
 * passes when its neighbour ran first is not watching what it says it is.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state: a
 * dialog on screen, a control appearing, a value in a field, an option in a
 * picker, a directory on disk, a row in the tree, a request in the server's
 * own log, a line on the sshd's stdout.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { bindEndpoint, settingsReady, VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'
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
  /** `127.0.0.1:<port>` — the same endpoint as a dial target, which is how
   *  the sshd names it when it forwards a channel there. */
  readonly address: string
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
    address: `127.0.0.1:${port}`,
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

/**
 * Set up the vault, through Settings, the way a person does.
 *
 * The import puts the VALUE of the export's bearer token in the vault and
 * leaves the NAME in the environment file (design §8.1), so there has to be a
 * vault for it to reach. These backends have no OS keystore, so it is the
 * passphrase one.
 *
 * One helper for both cases: the two differ in the route the fetch takes and
 * in nothing else, and a second copy of this would be a second answer to
 * "how does a person set up a vault".
 */
async function setUpVault(page: Page): Promise<void> {
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
}

/**
 * Open the API workbench and the import ask off its collections menu.
 *
 * The built-in collection is the first row a fresh stand has, and its arrival
 * is the listing's own account of having answered. Waiting on it is what makes
 * `defaultRoot` known by the time the ask opens, so a failure there reads as
 * "the listing never came back" rather than as a proposal bug.
 */
async function openImportAsk(page: Page): Promise<void> {
  await page.locator('.activity-bar button[data-action="api"]').click()
  const workbench = page.locator('.api-workbench')
  await expect(workbench).toBeVisible({ timeout: 15_000 })
  await expect(workbench.locator('.api-tree__row').first()).toBeVisible({ timeout: 15_000 })

  await workbench.locator('#api-collections-menu').click()
  await page.getByRole('menuitem', { name: 'Import collection…' }).click()
  await expect(page.getByRole('dialog').filter({ hasText: 'Import collection' })).toBeVisible()
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
    await setUpVault(page)

    // ── The workbench, and the ask off the collections menu ───────────────
    await openImportAsk(page)
    const workbench = page.locator('.api-workbench')
    const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })

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
    // it always writes. The case next door asserts the other value.
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

test.describe('an export arrives by URL through a connection', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  let exportServer: ExportServer
  let sshd: SshdFixture | null = null

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-import-conn-')) }
    exportServer = await startExportServer(postmanExport(UNREACHED_BASE_URL))
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterAll(async () => {
    sshd?.proc.kill()
    backend?.stop()
    await exportServer?.stop()
  })

  test('a pasted URL is fetched through the connection the person chose, and the collection keeps that route', async ({
    page,
  }) => {
    // A go build of the sshd, a real SSH handshake and an import do not fit
    // the suite's 30 s default. Every WAIT inside is still on observable
    // state; this is only the ceiling on the whole journey.
    test.setTimeout(180_000)

    const ep = await backend.start()

    // ── The far side ──────────────────────────────────────────────────────
    //
    // Its own HOME, BESIDE the backend's and never inside it: this is a
    // second machine's home and the two must not share `~/.nocx`
    // (sshd-fixture.ts says why). Its cwd is that home too, so nothing the
    // server spawns inherits the checkout.
    const remoteHome = mkdtempSync(join(disposable.root, 'remote-'))
    sshd = await startSshd({ home: remoteHome, cwd: remoteHome })

    // The backend's ssh client must accept this spawn's host key. REPLACED,
    // not appended: every spawn mints a fresh key and a stale line for a dead
    // one makes the backend refuse the connection. It goes into the home THIS
    // backend resolved, which is the disposable one — nothing here reads or
    // writes the developer's `~/.ssh`, and the fixture consults no ssh agent.
    const sshDir = join(backend.isolatedHome, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(join(sshDir, 'known_hosts'), sshd.knownHosts + '\n')

    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    await setUpVault(page)

    // ── The connection, seeded the way Settings would ─────────────────────
    //
    // Over the control plane rather than through the Connections page: what
    // this case is about is the ROUTE a fetch takes, and driving a profile
    // form would make the failure of an unrelated surface read as a routing
    // defect. The key is a FILE, so opening this connection needs no vault
    // preflight — the vault above is the import's, not this profile's.
    const profileName = `e2e-import-route-${Date.now()}`
    const created = await rpc<{ id: string }>(page, ep, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: sshd.host,
        port: sshd.port,
        user: 'e2e',
        keyPath: sshd.userKey,
      },
    })

    // ── The ask, and the address in it ────────────────────────────────────
    await openImportAsk(page)
    const workbench = page.locator('.api-workbench')
    const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })

    const exportURL = `${exportServer.origin}${EXPORT_PATH}`
    await page.locator('#api-import-paste').fill(exportURL)

    // ── THE PERSON PICKS THE CONNECTION ───────────────────────────────────
    //
    // The picker is read on every open of the ask (api-pane.tsx), so the
    // profile created a moment ago is in it. Waiting for its OPTION is
    // waiting on the list having arrived — the ask draws the control from the
    // URL alone, and selecting before the options land would select nothing.
    const route = page.locator('#api-import-route')
    await expect(route).toBeVisible()
    await expect(route.locator(`option[value="${created.id}"]`)).toBeAttached({ timeout: 15_000 })
    await route.selectOption(created.id)
    // The control's own account of holding the choice. Without this the steps
    // below would run against whatever the picker actually kept.
    await expect(route).toHaveValue(created.id)

    // ── …and where it goes, through the pencil ────────────────────────────
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

    // ── The folder arrives, which here is also the routing assertion ──────
    //
    // A connection route REFUSES when it cannot lease its profile and never
    // falls back to a local dial (apisend.ErrNoConnection), so this document
    // could not have arrived any other way than over the tunnel. The budget
    // is larger than the direct case's because an SSH connection is
    // established first — still a wait on the folder, not on a duration.
    await expect
      .poll(() => existsSync(collectionRoot), {
        message: `the routed import never produced ${collectionRoot}: ${backend.logTail()}`,
        timeout: 90_000,
      })
      .toBe(true)
    await expect(ask).toBeHidden()

    // ── The collection is in the tree ─────────────────────────────────────
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_COLLECTION_NAME }),
    ).toBeVisible({ timeout: 10_000 })
    await expect(
      workbench.locator('.api-tree__row').filter({ hasText: POSTMAN_REQUEST_NAME }),
    ).toBeVisible({ timeout: 10_000 })

    // ── THE BYTES CROSSED THE CONNECTION ──────────────────────────────────
    //
    // The far side's own account: `TCPIP=<host:port>` is printed once the
    // sshd has accepted a direct-tcpip channel and connected the address it
    // named. Both endpoints are on this machine's loopback, so the export
    // server cannot tell a routed request from a direct one — this line is
    // what does. It names the export server, so a channel forwarded somewhere
    // else would not satisfy it.
    await expect
      .poll(() => sshd?.lines().filter((l) => l === `TCPIP=${exportServer.address}`).length ?? 0, {
        message: `the sshd forwarded nothing to ${exportServer.address}: ${sshd?.lines().join('|')}`,
        timeout: 15_000,
      })
      .toBeGreaterThan(0)

    // And the request reached the far end of it — one GET, for that path.
    expect(exportServer.hits()).toEqual([`GET ${EXPORT_PATH}`])

    // ── AND THE COLLECTION KEEPS THE ROUTE IT ARRIVED THROUGH ─────────────
    //
    // The half that makes the feature worth having (spec §6). A collection
    // fetched through a connection whose environment said `direct` is a
    // collection where every request fails until the person sets by hand the
    // thing they had already told the import — and the profile is the SAME
    // one, because a route naming a different connection is a route to
    // somebody else's network.
    const env = JSON.parse(
      readFileSync(join(collectionRoot, 'environments', 'default.json'), 'utf8'),
    ) as EnvironmentRoute
    expect(env.route?.kind, 'the imported environment does not carry the connection route').toBe(
      'connection',
    )
    expect(env.route?.profileId).toBe(created.id)
  })
})
