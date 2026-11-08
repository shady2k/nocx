/**
 * ADR-8eej3e66 §"A person never sees busy under load" — end to end.
 *
 * The record's constraint table asks for one check: 50,000 entries churning, a
 * far host at 50 ms RTT, tabs switched continually, and legitimate traffic
 * beyond the credit window — with no toast and no refusal, healthy actions
 * SUCCEEDING within their declared deadlines, the blocked domain's actions
 * expiring as declared, and diagnostics holding the matching records. "Passing
 * by promptly timing everything out fails this check", which is why every
 * assertion below is a success or a declared expiry and never "no answer".
 *
 * What is staged rather than real, said once here:
 *
 *   - The far host is `cmd/e2e-sshd` behind `e2e/fault-proxy.ts` on loopback,
 *     with 25 ms added each way (50 ms RTT). Its HOME — and therefore its SFTP
 *     root — is the 50,000-entry tree this spec creates.
 *   - 50,000 entries are 25 directories of 2,000. ONE directory of 50,000 is
 *     deliberately not used: `DEFAULT_ENTRY_CAP` makes a listing over it
 *     `tooLarge` and stops polling it (internal/filesystem/local/local.go), so
 *     it would measure the cap instead of the load. The tree is what churns.
 *   - The blocked domain is a SECOND profile through a second proxy, which is
 *     black-holed partway through. A blackhole is a whole-TCP fault, so that
 *     tab's shell goes quiet too: nothing is typed into it after the block.
 *
 * No assertion waits for a duration: every wait is on an observable state
 * (`toHaveCount`, `toBeVisible`, `expect.poll`, a rendered row). The one
 * `slow(25)` is the far host's RTT — an ingredient, not a timing assumption.
 */
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import {
  appReadyForInput,
  expect,
  promptReady,
  resolveBackend,
  showSidebarView,
  test,
} from './harness'
import type { Page } from '@playwright/test'
import { startFaultProxy, type FaultProxy } from './fault-proxy'
import { startSshd, rpc, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'
import { callOperationDeclaration } from '../frontend/src/generated/operations'

const TAB = '.nocx-tab'
const TREE_DIRS = 25
const TREE_ENTRIES = 2_000
const BURST = 10
/** Declared bounds, read from the catalogue rather than restated: the spec must
 *  fail if the table and the product disagree about what "declared" means. */
const FILES_LIST = callOperationDeclaration('files.list')!

interface FilesCall {
  id: number
  method: string
  sessionId?: string
  bindingId?: string
  path?: string
  state?: string
  error?: unknown
}

interface WireStats {
  window: number | null
  inFlight: number
  peak: number
  filesListSends: number
  files: FilesCall[]
}

/** Rewrite a fixture's known_hosts line onto the port the client dials: the
 *  client reaches the fixture through the relay, so the fixture's own port
 *  would never match. */
function knownHostsFor(fixture: SshdFixture, port: number): string {
  const rest = fixture.knownHosts.slice(fixture.knownHosts.indexOf(' ') + 1)
  return `[127.0.0.1]:${port} ${rest}`
}

/** Write both trusted host keys into the home the stand's backend runs under.
 *  REPLACED, never appended: a stale line for a dead fixture makes the backend
 *  refuse the connection. */
function trustHostKeys(lines: string[]): void {
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), lines.join('\n') + '\n')
}

/** Activate the tab at `index` by clicking the strip, and wait until it is the
 *  selected one. A keyboard shortcut is not used: the strip's own order is the
 *  one the person sees, and the test should press what it can point at. */
async function activateTab(page: Page, index: number): Promise<void> {
  const tab = page.locator(TAB).nth(index)
  await expect(tab).toBeVisible()
  await tab.click()
  await expect(tab).toHaveAttribute('aria-selected', 'true')
}

/** Open a seeded profile through quick connect and wait for a real prompt. */
async function openConnection(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible()
  await search.fill(profileName)
  const savedRow = page.locator('.quick-connect__item:not(.quick-connect__item--system)', {
    hasText: profileName,
  })
  await expect(savedRow).toBeVisible({ timeout: 10_000 })
  await expect(savedRow).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .nocx-editor-input')).toBeVisible({ timeout: 30_000 })
}

/** Instrument the page's ONE control socket: how many ordinary frames are in
 *  flight, what window the backend advertised, and how many `files.list` calls
 *  the burst sent. The bound is on what is SENT (ADR-8eej3e66 §7), and this is
 *  the only place in the suite that can observe it. */
async function installWireInstrument(page: Page): Promise<void> {
  await page.addInitScript(() => {
    type Frame = {
      method?: string
      id?: number
      jsonrpc?: string
      params?: { sessionId?: string; bindingId?: string; path?: string }
    }
    // ONE ACCOUNT PER SOCKET. The page has more than one WebSocket over a run
    // — the dispatcher's connection, and the short-lived ones a spec's own
    // `rpc()` opens — and they have their own credit windows. Counting them
    // together would report a peak no single connection ever had.
    const accounts: WireStats[] = []
    ;(window as unknown as { __nocxWire: WireStats[] }).__nocxWire = accounts
    const Native = window.WebSocket
    const statsFor = (socket: object): WireStats => {
      const held = (socket as { __nocxStats?: WireStats }).__nocxStats
      if (held !== undefined) return held
      const fresh: WireStats = {
        window: null,
        inFlight: 0,
        peak: 0,
        filesListSends: 0,
        files: [],
      }
      ;(socket as { __nocxStats?: WireStats }).__nocxStats = fresh
      accounts.push(fresh)
      return fresh
    }
    // The advertisement is a RESULT: `transport.credit` is the request's
    // method and the answer carries only the id. So each account remembers
    // which ids asked for it.
    const creditIDs = new WeakMap<object, Set<number>>()
    const EXEMPT = new Set(['ack', 'rpc.cancel', 'transport.ping', 'transport.credit'])
    window.WebSocket = class extends Native {
      override send(data: Parameters<WebSocket['send']>[0]): void {
        if (typeof data === 'string') {
          try {
            const frame = JSON.parse(data) as Frame
            if (frame.method !== undefined && frame.jsonrpc === '2.0' && frame.id !== undefined) {
              const stats = statsFor(this)
              if (frame.method === 'files.list') stats.filesListSends++
              if (frame.method === 'files.open' || frame.method === 'files.list') {
                // Only fixture addresses and outcome discriminators, never
                // file contents, credentials or arbitrary response payloads.
                stats.files.push({
                  id: frame.id,
                  method: frame.method,
                  sessionId: frame.params?.sessionId,
                  bindingId: frame.params?.bindingId,
                  path: frame.params?.path,
                })
                if (stats.files.length > 128) stats.files.shift()
              }
              if (frame.method === 'transport.credit') {
                const ids = creditIDs.get(this) ?? new Set<number>()
                ids.add(frame.id)
                creditIDs.set(this, ids)
              }
              if (!EXEMPT.has(frame.method)) {
                stats.inFlight++
                stats.peak = Math.max(stats.peak, stats.inFlight)
              }
            }
          } catch {
            // A non-JSON frame is the data plane; nothing to count.
          }
        }
        super.send(data)
      }

      override addEventListener(
        type: string,
        listener: EventListenerOrEventListenerObject,
        options?: boolean | AddEventListenerOptions,
      ): void {
        if (type !== 'message') {
          super.addEventListener(type, listener, options)
          return
        }
        const wrapped = (event: Event): void => {
          const message = event as MessageEvent
          const stats = statsFor(this)
          if (typeof message.data === 'string') {
            try {
              const frame = JSON.parse(message.data) as {
                id?: number
                result?: { window?: number; bindingId?: string; state?: string }
                error?: unknown
              }
              if (frame.id !== undefined && creditIDs.get(this)?.delete(frame.id) === true) {
                if (typeof frame.result?.window === 'number') stats.window = frame.result.window
              }
              if (
                frame.id !== undefined &&
                (frame.result !== undefined || frame.error !== undefined)
              ) {
                stats.inFlight = Math.max(0, stats.inFlight - 1)
                const call = stats.files.find((entry) => entry.id === frame.id)
                if (call !== undefined) {
                  call.state = frame.result?.state
                  call.error = frame.error
                  if (frame.result?.bindingId !== undefined) {
                    call.bindingId = frame.result.bindingId
                  }
                }
              }
            } catch {
              // Not a control frame.
            }
          }
          if (typeof listener === 'function') listener(event)
          else listener.handleEvent(event)
        }
        super.addEventListener(type, wrapped, options)
      }
    }
  })
}

/** Read the instrumented counts from the page: the account of the socket that
 *  asked for the credit advertisement, i.e. the dispatcher's own connection. */
async function wireStats(page: Page): Promise<WireStats> {
  return page.evaluate(() => {
    // Installed by this spec before the page's dispatcher is created.
    const instrumented = window as unknown as { __nocxWire: WireStats[] }
    const accounts = instrumented.__nocxWire
    const renderer = accounts.find((a) => a.window !== null)
    return {
      window: renderer?.window ?? null,
      inFlight: renderer?.inFlight ?? 0,
      peak: renderer?.peak ?? 0,
      filesListSends: renderer?.filesListSends ?? 0,
      files: renderer?.files ?? [],
    }
  })
}

/** The `refusals:` and `credit overruns:` counts app.diagnostics renders. The
 *  server's own record is the second half of "no refusal": the renderer could
 *  swallow one, and this would still see it. */
async function diagnosticCounts(page: Page): Promise<{ refusals: number; overruns: number }> {
  const endpoint = await resolveBackend(page)
  const result = await rpc<{ text: string }>(page, endpoint, 'app.diagnostics', {})
  const read = (label: string): number => {
    const line = result.text.split('\n').find((l) => l.startsWith(label))
    if (line === undefined) return 0
    const n = Number(line.slice(label.length).trim())
    return Number.isFinite(n) ? n : 0
  }
  return {
    refusals: result.text.includes('refusals: none') ? 0 : read('refusals:'),
    overruns: result.text.includes('credit overruns: none') ? 0 : read('credit overruns:'),
  }
}

/**
 * Expand one directory in the Files panel and wait for one of its children.
 *
 * The row is clicked only once it is on screen and NOT busy: a directory row
 * that is still loading does not expand on a click (the panel leaves a busy
 * row alone), and a click that lands in that window is a silent no-op. If the
 * listing answers a state instead of rows, the failure says which — a timeout
 * on the child alone would not.
 */
async function expandRow(page: Page, suffix: string): Promise<void> {
  const row = page.locator(`[data-testid="files-row"][data-path$="/${suffix}"]`).first()
  await expect(row).toBeVisible({ timeout: FILES_LIST.maxMs })
  const treeItem = row.locator('.ui-tree-row')
  if ((await treeItem.getAttribute('aria-expanded')) !== 'true') {
    await expect(treeItem).not.toHaveAttribute('data-busy', 'true', { timeout: FILES_LIST.maxMs })
    await row.click()
  }
  await expect(treeItem).toHaveAttribute('aria-expanded', 'true', { timeout: FILES_LIST.maxMs })
  await expect(treeItem).not.toHaveAttribute('data-busy', 'true', { timeout: FILES_LIST.maxMs })
}

/** Wait for any row whose path is under `suffix`. The churner writes entries
 *  that sort ahead of the fixture's, so which names land on the first page is
 *  the store's business and not this assertion's. */
async function waitForChild(page: Page, suffix: string): Promise<void> {
  await expect(
    page.locator(`[data-testid="files-row"][data-path*="/${suffix}/"]`).first(),
  ).toBeVisible({ timeout: FILES_LIST.maxMs })
}

/** Expand one directory and wait for one of its children, failing with the
 *  state row's own words if the listing answered a state instead. */
async function expandAndSee(page: Page, name: string, child: string): Promise<void> {
  await expandRow(page, name)
  const childRow = page.locator(`[data-testid="files-row"][data-path$="/${name}/${child}"]`)
  const stateRow = page.locator(
    '[data-testid="files-state-timed-out"], [data-testid="files-state-error"], [data-testid="files-state-too-large"]',
  )
  await expect(childRow.or(stateRow).first()).toBeVisible({ timeout: FILES_LIST.maxMs })
  if ((await stateRow.count()) > 0) {
    throw new Error(
      `the listing of ${name} answered a state: ${await stateRow.first().innerText()}`,
    )
  }
}

/** The churner: 40 writes and 40 removals per tick, spread over the tree. Its
 *  only job is to keep the far store busy — nothing asserts on a duration. */
const CHURNER = `
const fs = require('node:fs')
const path = require('node:path')
const [, , tree, dirs] = process.argv
let n = 0
setInterval(() => {
  const d = path.join(tree, 'churn', 'd' + String(n % Number(dirs)).padStart(2, '0'))
  for (let i = 0; i < 40; i++) {
    try {
      fs.writeFileSync(path.join(d, 'c' + n + '-' + i + '.txt'), String(n))
      fs.rmSync(path.join(d, 'f' + String((n * 40 + i) % 2000).padStart(4, '0') + '.txt'), { force: true })
    } catch {}
  }
  n++
}, 100)
`

test('a person never sees busy under load', async ({ page }, testInfo) => {
  // The suite ceiling is 30 s and this is a load scenario; every wait inside is
  // on a state, so the ceiling is the only wall clock.
  test.setTimeout(240_000)
  const tree = testInfo.outputPath('far')
  const churner = path.join(testInfo.outputPath(), 'churn.cjs')
  let fixture: SshdFixture | null = null
  let healthy: FaultProxy | null = null
  let blocked: FaultProxy | null = null
  let churnerProc: ChildProcess | null = null
  let healthyProfile = ''
  let blockedProfile = ''
  let fault: 'pass' | 'blackhole' = 'pass'
  /** The ids the two profiles were created with. The stand's home is shared
   *  with every other spec in the run, so they are removed by ID in the
   *  finally — `profiles.list` answers an ARRAY (contracts/profiles.list has no
   *  result schema: the method returns the list itself), and a cleanup that
   *  reads `listed.profiles` silently deletes nothing and leaves two saved
   *  connections for the next spec's picker. That is exactly how this spec
   *  broke e2e/quick-connect.spec.ts on CI. */
  const profileIDs: string[] = []

  try {
    // ── the corpus: 50,000 entries in one place, churning ────────────────
    for (let d = 0; d < TREE_DIRS; d++) {
      const dir = path.join(tree, 'churn', `d${String(d).padStart(2, '0')}`)
      mkdirSync(dir, { recursive: true })
      for (let i = 0; i < TREE_ENTRIES; i++) {
        writeFileSync(path.join(dir, `f${String(i).padStart(4, '0')}.txt`), 'x')
      }
    }
    writeFileSync(churner, CHURNER)
    churnerProc = spawn(process.execPath, [churner, tree, String(TREE_DIRS)], {
      stdio: 'ignore',
      detached: false,
    })

    // ── one far host, two networks, two profiles ─────────────────────────
    fixture = await startSshd({ home: tree, cwd: tree })
    healthy = await startFaultProxy(fixture.host, fixture.port)
    blocked = await startFaultProxy(fixture.host, fixture.port)
    trustHostKeys([knownHostsFor(fixture, healthy.port), knownHostsFor(fixture, blocked.port)])

    await installWireInstrument(page)
    await page.goto('/')
    await appReadyForInput(page)
    const endpoint = await resolveBackend(page)
    const createProfile = async (name: string, port: number): Promise<string> => {
      const created = await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
        type: 'ssh',
        name,
        options: {
          host: '127.0.0.1',
          port,
          user: 'e2e',
          keyPath: fixture!.userKey,
          desiredMode: 'script',
        },
      })
      profileIDs.push(created.id)
      return created.id
    }
    const stamp = Date.now()
    healthyProfile = `e2e-load-healthy-${stamp}`
    blockedProfile = `e2e-load-blocked-${stamp}`
    await createProfile(healthyProfile, healthy.port)
    await createProfile(blockedProfile, blocked.port)

    // ── the two far tabs, and the Files panel following the active one ───
    await openConnection(page, healthyProfile)
    await expect(page.locator(TAB)).toHaveCount(2)
    await showSidebarView(page, 'files')
    await promptReady(page)

    // Load the blocked tab's tree while its network still passes: the churn
    // directory's 25 children cost one listing, and each unexpanded child is a
    // listing the blocked phase can still ask for. The rows are addressed by
    // the path they name, never by their text: "d01" is a name this tree has
    // in every directory.
    await expandAndSee(page, 'churn', 'd24')

    // ── the load: the far host is 50 ms away, and the burst exceeds the
    // ── credit window ───────────────────────────────────────────────────
    const before = await diagnosticCounts(page)
    healthy.slow(25)

    // Ten expands with no await between them: more legitimate calls than the
    // advertised window holds. The renderer queues the rest; the server never
    // hears a frame past its window, which is the whole point of §7.
    const burst = Array.from({ length: BURST }, (_, d) => `d${String(d).padStart(2, '0')}`)
    for (const name of burst) {
      const row = page.locator(`[data-testid="files-row"][data-path$="/churn/${name}"]`)
      await expect(row).toBeVisible({ timeout: FILES_LIST.maxMs })
      await expect(row).not.toHaveAttribute('data-busy', 'true', { timeout: FILES_LIST.maxMs })
      await row.click({ noWaitAfter: true })
    }
    // Every one of them is expanded AND settled inside the declaration's own
    // bound: success, not a timeout. The child row is addressed by PREFIX, not
    // by a name: the churner writes c*.txt entries that sort ahead of f*.txt,
    // so which names are on the first page is the store's business and not
    // this assertion's.
    for (const name of burst) {
      // The state attributes live on the kit row inside the panel's wrapper
      // (files-row > ui-tree-row[role=treeitem]); the wrapper carries the path.
      const folderRow = page.locator(`[data-testid="files-row"][data-path$="/churn/${name}"]`)
      const treeItem = folderRow.locator('.ui-tree-row')
      await expect(treeItem).toHaveAttribute('aria-expanded', 'true', { timeout: FILES_LIST.maxMs })
      await expect(treeItem).not.toHaveAttribute('data-busy', 'true', { timeout: FILES_LIST.maxMs })
      await waitForChild(page, `churn/${name}`)
    }
    const stats = await wireStats(page)
    expect(stats.window, 'the backend advertised a credit window').not.toBeNull()
    // More legitimate calls were made than the advertised window holds, and
    // the renderer never sent more than the window: it held the rest, which is
    // §7's whole model. Both halves are asserted — a run that never exceeded
    // the window would prove nothing about queueing.
    expect(stats.filesListSends).toBeGreaterThan(stats.window ?? 0)
    expect(stats.peak).toBeLessThanOrEqual(stats.window ?? 0)
    expect(stats.peak).toBeGreaterThan(1)

    // ── tabs switched continually, a person acting throughout ────────────
    for (let round = 0; round < 6; round++) {
      await activateTab(page, round % 2)
      await promptReady(page)
    }

    // ── the blocked domain expires as declared ───────────────────────────
    // A second far tab, opened here rather than at the start: it is its own
    // failure domain, and opening it now means the panel scopes to it once —
    // no back-and-forth re-scoping of two far bindings while the network is
    // being taken away.
    await openConnection(page, blockedProfile)
    await expect(page.locator(TAB)).toHaveCount(3)
    await showSidebarView(page, 'files')
    await promptReady(page)
    // The panel may have collapsed back to its root, so the churn directory is
    // expanded here — while the blocked tab's network still passes, because
    // after the blackhole nothing on it can list.
    await expandRow(page, 'churn')
    const pending = page.locator('[data-testid="files-row"][data-path$="/churn/d20"]')
    await expect(pending).toBeVisible({ timeout: FILES_LIST.maxMs })
    const pendingItem = pending.locator('.ui-tree-row')
    await expect(pendingItem).toHaveAttribute('aria-expanded', 'false')
    await expect(pendingItem).not.toHaveAttribute('data-busy', 'true')
    const beforeBlock = await wireStats(page)
    const blockedBinding = beforeBlock.files.findLast((call) => call.method === 'files.open')
    expect(blockedBinding?.bindingId, 'the blocked tab opened its Files binding').toBeTruthy()
    const pendingPath = await pending.getAttribute('data-path')
    blocked.blackhole()
    fault = 'blackhole'
    await pending.click({ noWaitAfter: true })
    await expect(pendingItem).toHaveAttribute('aria-expanded', 'true')
    await expect
      .poll(async () => {
        const calls = (await wireStats(page)).files
        return calls.some(
          (call) =>
            call.method === 'files.list' &&
            call.bindingId === blockedBinding?.bindingId &&
            call.path === pendingPath &&
            call.id > (beforeBlock.files.at(-1)?.id ?? 0),
        )
      })
      .toBe(true)
    await expect(page.locator('[data-testid="files-state-timed-out"]')).toBeVisible({
      timeout: FILES_LIST.maxMs + 10_000,
    })
    await expect(page.locator('[data-testid="files-retry"]')).toBeVisible()

    // ── the healthy domain still answers beside it ───────────────────────
    //
    // The panel re-scopes when the tab changes, and its tree may come back
    // collapsed, so the directory is expanded as part of the action. What is
    // asserted is the OUTCOME: a listing on the healthy far domain completes
    // inside the declaration's own bound while the other domain cannot answer
    // at all.
    await activateTab(page, 1)
    await showSidebarView(page, 'files')
    await expandRow(page, 'churn')
    await expandRow(page, 'churn/d21')
    await waitForChild(page, 'churn/d21')

    // ── and the record: no refusal, no overrun, no busy sentence, no toast ─
    const after = await diagnosticCounts(page)
    expect(after.refusals).toBe(before.refusals)
    expect(after.overruns).toBe(before.overruns)
    await expect(page.locator('.ui-toast__message')).toHaveCount(0)
    await expect(page.getByText(/the terminal is busy/i)).toHaveCount(0)
    await expect(page.getByText(/control plane busy/i)).toHaveCount(0)
    await expect(page.getByText(/try again in a moment/i)).toHaveCount(0)
  } catch (error) {
    // The fixture's finally closes both SSH tabs. The shared reporter runs
    // afterwards, when its snapshot and last frames no longer show the
    // failed directory or its response. Preserve this boundary first.
    await testInfo.attach('files-before-teardown', {
      contentType: 'application/json',
      body: JSON.stringify({
        fault,
        wire: await wireStats(page),
        tree: await page.getByRole('tree', { name: 'Files', exact: true }).ariaSnapshot(),
      }),
    })
    throw error
  } finally {
    churnerProc?.kill('SIGKILL')
    await blocked?.close()
    await healthy?.close()
    fixture?.proc.kill('SIGKILL')
    // The tree is 200 MB: a leak here is 200 MB of `eslint .` input afterwards.
    rmSync(tree, { recursive: true, force: true })
    rmSync(churner, { force: true })
    // The stand's home is shared with every other spec, so the profiles go —
    // by id, the way e2e/ssh-reconnect.spec.ts removes its own.
    try {
      const endpoint = await resolveBackend(page)
      for (const id of profileIDs) {
        await rpc(page, endpoint, 'profiles.delete', { id })
      }
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
  }
})
