/**
 * e2e: a run you can watch and stop, and a failure you can read
 * (epic nocx-pgp9c). This is that epic's DONE WHEN, and it is written to
 * watch a PERSON do the three things it exists for:
 *
 *   1. press Send and see a row appear before any answer exists;
 *   2. press Stop and see that same row settle as stopped, without ever
 *      being worded as a failure;
 *   3. send at a name that does not resolve, and read the phase and the
 *      request text off the row.
 *
 * WHY THIS FILE. AGENTS.md testing rule 2: `deadcode` and coverage are
 * floors, and neither can report a feature that is MISSING — only that
 * written code is used. Every unit test underneath this passes a fixture to a
 * store or a component; none of them can say whether a person can reach the
 * button, whether the button they reach is the right one, or whether pressing
 * it ends the exchange rather than only the row. So every step below goes
 * through the seam a person reaches — the activity-bar entry, the folder ask,
 * a row in the tree, the one button on the request line, the run card — and
 * nothing here calls ApiClient or the control plane to arrange a state the
 * product is supposed to arrange.
 *
 * NOTHING HERE WAITS ON A DURATION. There is no `waitForTimeout` in this
 * file. "In flight" is not a moment this spec hopes to catch: a server it
 * owns is HOLDING the exchange, so the pending row is on screen for as long
 * as the spec chooses, and every other wait is on an observable state — a
 * dialog, a row, a card's own `data-outcome`. A spec that needs a slow
 * machine to pass is broken on a fast one too; it has only not been caught
 * yet.
 *
 * THE SHARED STAND IS ENOUGH, unlike api-testing.spec.ts. That one imports a
 * Postman export carrying a credential, so it needs a vault it set up itself
 * and an isolated home it can read back. Nothing here has a secret in it: the
 * requests carry no auth, the collection is a folder this spec writes, and
 * the only durable thing it leaves behind is that folder — under the OS temp
 * directory, removed afterwards.
 */
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { test, expect } from './harness'
import { startStallingServer, type StallingServer } from './fixtures/api-stalling-server'

/** A name reserved never to resolve (RFC 2606). The failure it produces is
 *  the same one the sender's own tests use, and it needs no network: the
 *  resolver refuses it locally. */
const UNRESOLVABLE = 'http://nocx-e2e-no-such-host.invalid/v1'

/** The request that stalls, and the one that cannot be reached. Two files in
 *  one collection, so the whole spec is one folder and one open. */
const SLOW = 'slow'
const DEAD = 'dead'

/**
 * A collection folder in the product's own on-disk format.
 *
 * WRITTEN RATHER THAN IMPORTED, and that is a deliberate difference from
 * api-testing.spec.ts. That spec's subject is the import; this one's is what
 * happens between pressing Send and reading the answer, and an import in
 * front of it would be a second feature this test could fail for. The folder
 * is opened THROUGH THE UI all the same — the ask, the field, the button —
 * because "can a person get to it" is part of what is being checked.
 */
function writeCollection(root: string, slowUrl: string): void {
  mkdirSync(root, { recursive: true })
  const write = (rel: string, body: unknown): void =>
    writeFileSync(join(root, rel), JSON.stringify(body), 'utf8')

  write('nocx-collection.json', { schemaVersion: 1, name: 'lifecycle' })
  write(`${SLOW}.json`, {
    id: 'r-slow',
    name: SLOW,
    method: 'GET',
    url: `${slowUrl}/hold`,
    headers: [{ name: 'X-Probe', value: 'lifecycle', enabled: true }],
    query: [],
    body: { kind: 'none' },
    auth: { kind: 'none' },
  })
  write(`${DEAD}.json`, {
    id: 'r-dead',
    name: DEAD,
    method: 'POST',
    url: UNRESOLVABLE,
    headers: [],
    query: [],
    body: { kind: 'raw', text: '{"who":"nobody"}' },
    auth: { kind: 'none' },
  })
}

test.describe('an API run you can watch, stop, and read a failure off', () => {
  // The workbench is three columns — tree, request, runs — and all three have
  // to be on screen for a person to do this. The container's default viewport
  // is narrower than the shipped app's window.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: string
  let collectionRoot: string
  let server: StallingServer

  test.beforeAll(async () => {
    disposable = mkdtempSync(join(tmpdir(), 'nocx-e2e-runs-'))
    collectionRoot = join(disposable, 'lifecycle')
    server = await startStallingServer()
    writeCollection(collectionRoot, server.baseUrl)
  })

  test.afterAll(async () => {
    await server?.stop()
    rmSync(disposable, { recursive: true, force: true })
  })

  test('Send shows a pending row, Stop settles it as stopped, and a name that does not resolve reads as a run with its phase and its request', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // ── The workbench, and the folder in it ─────────────────────────────────
    //
    // The activity-bar entry, which design §9.2 says opens or focuses the
    // pane. Reaching the pane any other way would prove the pane works and
    // say nothing about whether it can be got to.
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })

    // The folder ask lives in the collections menu — the ⋮ beside the
    // section, not a button on the panel. A panel that wears its own form is
    // what that menu exists to end, and a spec that clicks a button nobody
    // has any more is a spec asserting the shape of an older product.
    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Open folder…' }).click()
    const folderAsk = page.getByRole('dialog').filter({ hasText: 'Open a collection folder' })
    await expect(folderAsk).toBeVisible()
    await page.locator('#api-collection-path').fill(collectionRoot)
    await folderAsk.getByRole('button', { name: 'Open', exact: true }).click()

    const slowRow = workbench.locator('.api-tree__row').filter({ hasText: SLOW })
    await expect(slowRow).toBeVisible({ timeout: 10_000 })
    await slowRow.click()
    await expect(workbench.locator('#api-url')).toHaveValue(`${server.baseUrl}/hold`)

    // ── 1. The row exists before the answer does ────────────────────────────
    //
    // This is the whole defect in one assertion. The row used to be built
    // from the RESULT of the send, so it could not exist before the answer
    // did: a person pressing Send saw a disabled button and an empty column.
    await workbench.getByRole('button', { name: 'Send', exact: true }).click()

    const run = workbench.locator('.api-run').first()
    await expect(run).toHaveAttribute('data-outcome', 'pending', { timeout: 15_000 })
    // …and the row is worth looking at while it is pending: it says what is
    // being sent, not merely that something is.
    await expect(run).toContainText('GET')
    await expect(run).toContainText(`${server.baseUrl}/hold`)
    // The exchange is genuinely outstanding — the server is holding it, which
    // is what makes the state above a state rather than a coincidence.
    await expect.poll(() => server.holding(), { timeout: 15_000 }).toBe(1)

    // ── 2. Stop, and the same row settles as stopped ────────────────────────
    //
    // The button IS the Stop: one control for one exchange. Its presence is
    // the assertion — a Send button here would mean the line never noticed
    // its own run was in flight.
    const stop = workbench.getByRole('button', { name: 'Stop', exact: true })
    await expect(stop).toBeVisible()
    await expect(stop).toBeEnabled()
    await expect(workbench.getByRole('button', { name: 'Send', exact: true })).toHaveCount(0)
    await stop.click()

    await expect(run).toHaveAttribute('data-outcome', 'stopped', { timeout: 15_000 })
    // A STOP IS NOT A FAILURE, in the product's own words. The person ended
    // it on purpose, and a red card telling them it went wrong would be the
    // product disagreeing with them about something they did.
    await expect(run).toContainText('Stopped')
    await expect(run).not.toContainText('did not go out')
    await expect(run.locator('.ui-status-card')).not.toHaveAttribute('data-tone', 'danger')
    // AND THE EXCHANGE ACTUALLY ENDED. Without this the two assertions above
    // would pass on a renderer that relabelled a row while the request went
    // on being served — a button that lies.
    await expect.poll(() => server.abandoned(), { timeout: 15_000 }).toBe(1)
    // The line offers a send again, because nothing of this request is in
    // flight any more.
    await expect(workbench.getByRole('button', { name: 'Send', exact: true })).toBeVisible()

    // ── 3. A failure is a run you can read ──────────────────────────────────
    //
    // A name that does not resolve. Before this it came back as a JSON-RPC
    // error — one sentence, no request, no route, no timing — while the
    // sender was holding all of it at the moment it failed.
    const deadRow = workbench.locator('.api-tree__row').filter({ hasText: DEAD })
    await deadRow.click()
    await expect(workbench.locator('#api-url')).toHaveValue(UNRESOLVABLE)
    await workbench.getByRole('button', { name: 'Send', exact: true }).click()

    const failed = workbench.locator('.api-run').first()
    await expect(failed).toHaveAttribute('data-outcome', 'failed', { timeout: 20_000 })
    // WHERE it stopped, in the product's words rather than the wire's token.
    await expect(failed).toContainText('the name did not resolve')

    // AND WHAT WENT OUT. The raw view is reachable on a run that never
    // reached a server — which is exactly the run that most needs it — and
    // what it shows is the sender's own account of the text it composed.
    await failed.getByRole('tab', { name: 'Raw' }).click()
    const rawRequest = failed.locator('[aria-label="Raw request"]')
    await expect(rawRequest).toBeVisible()
    await expect(rawRequest).toContainText('POST /v1 HTTP/1.1')
    await expect(rawRequest).toContainText('Host: nocx-e2e-no-such-host.invalid')
    await expect(rawRequest).toContainText('{"who":"nobody"}')
    // There is no response side, and none is drawn: a heading over an empty
    // block would say a server replied with nothing, which is a different
    // fact from not replying.
    await expect(failed.locator('[aria-label="Raw response"]')).toHaveCount(0)

    // The stopped run is still there, above nothing and below the new one:
    // two questions asked from two different requests keep their own answers
    // (the run list is per request), so this one shows only the dead run.
    await expect(workbench.locator('.api-run')).toHaveCount(1)
  })
})
