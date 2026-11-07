/**
 * e2e: a restore that cannot read the live inventory, or cannot claim a
 * session, does not open a second shell (nocx-3an1s.1.5).
 *
 * WHAT A USER CAN DO THAT THEY COULD NOT BEFORE, and what this spec watches:
 * reload a window holding ten integrated local panes while the coordinator's
 * `sessions.live` answer is lost, or while its claims come back "did not
 * start", and come back to THE SAME ten shells — same session ids, same
 * PIDs, same helper inventory — with no replacement shell opened and no
 * window-budget refusal. The pane is usable afterwards, or (when the answer
 * never comes) it shows the existing attach retry and still opens nothing.
 *
 * # Why this is an e2e against the real backend
 *
 * The defect is a count of helper sessions. It cannot be observed anywhere
 * below `cmd/nocx-server`: the unit tests can say the renderer did not call
 * `open`, and only the real coordinator and its embedded helper can say how
 * many shells exist, with which PIDs, and whether the 512 MiB reservation
 * budget was exhausted. The owner measured exactly that shape — ten panes,
 * one failed `sessions.live`, eighteen helper sessions, two panes showing
 * `window_budget` at 528482304/536870912 bytes — and this spec is the same
 * arrangement with the same instrument.
 *
 * # The fault, and why it is injected in the renderer's socket
 *
 * `sessions.live` is the first request a reload makes, and there is no
 * supported way to make the shipped server fail exactly one of them. So the
 * page's WebSocket is wrapped (the pattern agent-redraw-mode.spec.ts and
 * several others already use) and ONE request is answered with a JSON-RPC
 * error the server could have sent. Everything after it is real: the backend,
 * the helper, the PTY, the layout chain, the renderer. The answers are the
 * backend's own — -32603 for a lost read, -32005 with the catalogue's
 * `not-started` reason for a call whose turn ran out before it started
 * (contracts/operation.expired.schema.json) — so the renderer is given a
 * fault it already knows how to classify, and this spec does not invent a
 * new one. There is no capacity refusal to inject: ADR-8eej3e66 stage 8
 * removed it, and a spec that still injected the busy answer would be staging
 * something the server cannot give.
 *
 * # The positive control
 *
 * `open` is counted on the same wrapped socket. Zero opens across a reload is
 * the assertion; the last test shows the counter is live by proving that a
 * pane whose session is genuinely absent still gets a fresh shell.
 */
import { expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  VaultBackend,
  bindEndpoint,
  endDaemonsUnder,
  endUnattachedSession,
  openControlPlane,
  promptReady,
  type BackendEndpoint,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

/** Lazily: the stand is started by globalSetup, which runs after Playwright
 *  has collected this file. */
const serverBin = () => readStand().server

const TABS = '.nocx-tab'
const ATTACH_CARD = '.nocx-attach-offer'
const LIVE_GRID = '.pane.active .terminal-live-grid'

/** Ten, because that is the number the owner measured: ten live shells plus
 *  ten replacements is nineteen against a budget of eighteen, so the red run
 *  shows both the duplicated sessions and the `window_budget` refusal. */
const PANES = 10

interface LiveSession {
  sessionId: string
  paneId: string | null
  replayFrom: number
}

interface InventorySession {
  hostSessionId: { generation: string; session: string }
  launch?: { pid: number }
}

/** What the wrapped socket is asked to do to the next document. */
type FaultMode = 'off' | 'inventory-once' | 'inventory-always' | 'attach-not-started'

/**
 * The page's socket, wrapped once per document. Inert until a mode is set, so
 * the first load — the one that opens the ten panes — is untouched.
 */
async function installFaultShim(page: Page): Promise<void> {
  await page.addInitScript(() => {
    type FaultWindow = { __nocxFaultMode?: string; __nocxOpens?: number }
    const fault = window as unknown as FaultWindow
    fault.__nocxOpens = 0
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      override send(data: Parameters<WebSocket['send']>[0]): void {
        if (typeof data !== 'string') {
          super.send(data)
          return
        }
        let message: { id?: number; method?: string }
        try {
          message = JSON.parse(data) as { id?: number; method?: string }
        } catch {
          super.send(data)
          return
        }
        if (message.method === undefined) {
          super.send(data)
          return
        }
        if (message.method === 'open') fault.__nocxOpens = (fault.__nocxOpens ?? 0) + 1
        const mode = fault.__nocxFaultMode ?? 'off'
        let error: unknown = null
        if (message.method === 'sessions.live' && mode === 'inventory-once') {
          fault.__nocxFaultMode = 'off'
          error = { code: -32603, message: 'injected: the live inventory could not be read' }
        } else if (message.method === 'sessions.live' && mode === 'inventory-always') {
          error = {
            code: -32005,
            message: 'The call did not start',
            data: { reason: 'not-started' },
          }
        } else if (message.method === 'attach' && mode === 'attach-not-started') {
          error = {
            code: -32005,
            message: 'The call did not start',
            data: { reason: 'not-started' },
          }
        }
        if (error === null) {
          super.send(data)
          return
        }
        // Answered the way the server would answer it: a response frame on
        // the same socket, carrying the request's own id.
        queueMicrotask(() => {
          this.dispatchEvent(
            new MessageEvent('message', {
              data: JSON.stringify({ jsonrpc: '2.0', id: message.id, error }),
            }),
          )
        })
      }
    }
  })
}

/** Arm a fault for the NEXT document, which is the reload under test. */
async function armFault(page: Page, mode: FaultMode): Promise<void> {
  await page.addInitScript((m: string) => {
    ;(window as unknown as { __nocxFaultMode?: string }).__nocxFaultMode = m
  }, mode)
}

/** Change the fault in the document already running (the "it clears" half). */
async function setFault(page: Page, mode: FaultMode): Promise<void> {
  await page.evaluate((m: string) => {
    ;(window as unknown as { __nocxFaultMode?: string }).__nocxFaultMode = m
  }, mode)
}

async function opensRequested(page: Page): Promise<number> {
  return page.evaluate(() => (window as unknown as { __nocxOpens?: number }).__nocxOpens ?? 0)
}

async function liveSessions(endpoint: BackendEndpoint): Promise<LiveSession[]> {
  const wire = await openControlPlane(endpoint.port, endpoint.token)
  try {
    const answer = (await wire.call('sessions.live', {})) as { sessions: LiveSession[] }
    return answer.sessions
  } finally {
    wire.close()
  }
}

async function inventory(endpoint: BackendEndpoint): Promise<InventorySession[]> {
  const wire = await openControlPlane(endpoint.port, endpoint.token)
  try {
    const answer = (await wire.call('sessions.inventory', {})) as {
      sessions: InventorySession[]
    }
    return answer.sessions
  } finally {
    wire.close()
  }
}

/** The helper's own local launch records: the shells, as the daemon sees them.
 *
 *  SORTED, because the inventory states no order — the helper's map does not
 *  promise one and two reads of an unchanged set returned it differently on
 *  the first run of this spec. The assertion is about WHICH shells exist, and
 *  an order-sensitive equality would report a reordering as a new shell. */
function localShells(entries: InventorySession[]): { session: string; pid: number }[] {
  return entries
    .filter((entry) => entry.launch !== undefined)
    .map((entry) => ({ session: entry.hostSessionId.session, pid: entry.launch!.pid }))
    .sort((a, b) => (a.session < b.session ? -1 : a.session > b.session ? 1 : 0))
}

function sorted(values: (string | number)[]): (string | number)[] {
  return [...values].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
}

/** Is this pid there, asked of the KERNEL? */
function aliveFromTheOS(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

async function countWindowBudgetRefusals(page: Page): Promise<number> {
  const text = await page.locator('body').innerText()
  return text.split('window_budget').length - 1
}

/** Clear the screen-level notifications so they cannot sit over the card.
 *  A saturation refusal raises a sticky `danger` toast (dispatcher.ts), and
 *  the retry under test is a real click a person would have to reach. */
async function dismissToasts(page: Page): Promise<void> {
  const dismiss = page.getByRole('button', { name: 'Dismiss notification' })
  for (let i = 0; i < 20; i++) {
    if ((await dismiss.count()) === 0) return
    await dismiss.first().click()
  }
}

/** Open the requested number of integrated local panes, all on one backend. */
async function openPanes(page: Page, count: number): Promise<void> {
  for (let i = 1; i < count; i++) {
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.locator(TABS)).toHaveCount(i + 1)
    await promptReady(page)
  }
}

/** The active pane back at a prompt, whatever the real plane answered.
 *
 *  The fix does not make the coordinator faster: ten panes reclaiming at once
 *  can still answer "did not start" while they wait for their sessions, and
 *  the measured no-fault reproduction of this defect began exactly there. What
 *  the fix changes is the consequence — such a pane keeps its session and shows
 *  the existing retry instead of opening a second shell. So the spec accepts
 *  both honest outcomes and, when the card is up, presses it the way a person
 *  would. That click is the product's own action, not a retry of a failed
 *  assertion. */
async function ensureActivePaneUsable(page: Page): Promise<void> {
  const card = page.locator('.pane.active ' + ATTACH_CARD)
  const editor = page.locator('.pane.active .nocx-editor-input')
  await expect
    .poll(async () => (await card.count()) > 0 || (await editor.isVisible()), { timeout: 30_000 })
    .toBe(true)
  if ((await card.count()) > 0) {
    await dismissToasts(page)
    await card.locator('button').click()
    await expect(card).toHaveCount(0, { timeout: 30_000 })
  }
  await promptReady(page)
}

/** Type a command and wait for its own output on the live screen. */
async function runMarker(page: Page, marker: string): Promise<void> {
  await promptReady(page)
  await page.keyboard.type(`echo ${marker}`)
  await page.keyboard.press('Enter')
  await expect(page.locator(LIVE_GRID)).toContainText(marker, { timeout: 30_000 })
}

const test = base

test.describe('a restore whose live inventory cannot be read (nocx-3an1s.1.5)', () => {
  let home: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-restore-uncertain-')) }
    backend = new VaultBackend(serverBin(), home)
  })

  test.afterEach(async () => {
    backend?.stop()
    let isolatedHome = ''
    try {
      isolatedHome = backend.isolatedHome
    } catch {
      /* the backend never started, so it started no daemon either */
    }
    await endDaemonsUnder(isolatedHome)
  })

  test('one failed sessions.live on reload keeps every session and opens no shell', async ({
    page,
  }) => {
    test.setTimeout(180_000)
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await installFaultShim(page)
    await page.goto('/')
    await promptReady(page)
    await expect(page.locator(TABS)).toHaveCount(1, { timeout: 30_000 })
    await openPanes(page, PANES)
    await expect(page.locator(TABS)).toHaveCount(PANES, { timeout: 30_000 })

    const beforeLive = await liveSessions(endpoint)
    const beforeInventory = await inventory(endpoint)
    expect(beforeLive).toHaveLength(PANES)
    expect(beforeInventory).toHaveLength(PANES)
    const beforeShells = localShells(beforeInventory)
    expect(beforeShells).toHaveLength(PANES)
    for (const shell of beforeShells) expect(aliveFromTheOS(shell.pid)).toBe(true)

    // ── THE RELOAD UNDER TEST: exactly ONE sessions.live answer is lost ───
    await armFault(page, 'inventory-once')
    await page.reload()
    await ensureActivePaneUsable(page)
    await expect(page.locator(TABS)).toHaveCount(PANES, { timeout: 30_000 })

    // NO REPLACEMENT SHELL WAS ASKED FOR. This is the defect in one number:
    // before the fix, every restored pane sent `open` here.
    expect(await opensRequested(page)).toBe(0)
    expect(await countWindowBudgetRefusals(page)).toBe(0)

    // The same sessions, the same panes, the same shells, the same PIDs.
    const afterLive = await liveSessions(endpoint)
    expect(sorted(afterLive.map((s) => s.sessionId))).toEqual(
      sorted(beforeLive.map((s) => s.sessionId)),
    )
    expect(sorted(afterLive.map((s) => s.paneId ?? ''))).toEqual(
      sorted(beforeLive.map((s) => s.paneId ?? '')),
    )
    const afterInventory = await inventory(endpoint)
    expect(afterInventory).toHaveLength(PANES)
    expect(localShells(afterInventory)).toEqual(beforeShells)
    for (const shell of localShells(afterInventory)) expect(aliveFromTheOS(shell.pid)).toBe(true)

    // And the pane is a working terminal again, not merely a surviving row.
    await runMarker(page, `NOCX-RESTORED-${Date.now().toString(36)}`)

    // ── A SECOND reload with the same fault leaves everything where it was ─
    await armFault(page, 'inventory-once')
    await page.reload()
    await ensureActivePaneUsable(page)
    await expect(page.locator(TABS)).toHaveCount(PANES, { timeout: 30_000 })
    expect(await opensRequested(page)).toBe(0)
    expect(await countWindowBudgetRefusals(page)).toBe(0)
    expect(await liveSessions(endpoint)).toHaveLength(PANES)
    expect(await inventory(endpoint)).toHaveLength(PANES)
    expect(localShells(await inventory(endpoint))).toEqual(beforeShells)
  })

  test('a claim that did not start keeps the pane on its own session and the retry takes it back', async ({
    page,
  }) => {
    test.setTimeout(180_000)
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await installFaultShim(page)
    await page.goto('/')
    await promptReady(page)
    await expect(page.locator(TABS)).toHaveCount(1, { timeout: 30_000 })
    await openPanes(page, PANES)
    await expect(page.locator(TABS)).toHaveCount(PANES, { timeout: 30_000 })

    const beforeLive = await liveSessions(endpoint)
    const beforeShells = localShells(await inventory(endpoint))
    expect(beforeLive).toHaveLength(PANES)
    expect(beforeShells).toHaveLength(PANES)

    // ── THE RELOAD UNDER TEST: every claim answers "did not start" ─────────
    await armFault(page, 'attach-not-started')
    await page.reload()
    await expect(page.locator(TABS)).toHaveCount(PANES, { timeout: 30_000 })
    await expect(page.locator(ATTACH_CARD)).toHaveCount(PANES, { timeout: 30_000 })

    // An answer with no verdict is uncertainty, not absence: nothing opened.
    expect(await opensRequested(page)).toBe(0)
    expect(await countWindowBudgetRefusals(page)).toBe(0)
    expect(await liveSessions(endpoint)).toHaveLength(PANES)
    expect(localShells(await inventory(endpoint))).toEqual(beforeShells)

    // ── THE PERSON RETRIES, and the plane is answering again ──────────────
    await setFault(page, 'off')
    await dismissToasts(page)
    await page.locator('.pane.active ' + ATTACH_CARD + ' button').click()
    await expect(page.locator('.pane.active ' + ATTACH_CARD)).toHaveCount(0, {
      timeout: 30_000,
    })
    expect(await opensRequested(page)).toBe(0)
    const afterLive = await liveSessions(endpoint)
    expect(sorted(afterLive.map((s) => s.sessionId))).toEqual(
      sorted(beforeLive.map((s) => s.sessionId)),
    )
    expect(localShells(await inventory(endpoint))).toEqual(beforeShells)
    await runMarker(page, `NOCX-RETRIED-${Date.now().toString(36)}`)
  })

  test('an inventory that stays unavailable keeps the panes and their retry', async ({ page }) => {
    test.setTimeout(180_000)
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await installFaultShim(page)
    await page.goto('/')
    await promptReady(page)
    await expect(page.locator(TABS)).toHaveCount(1, { timeout: 30_000 })
    await openPanes(page, PANES)
    await expect(page.locator(TABS)).toHaveCount(PANES, { timeout: 30_000 })

    const beforeLive = await liveSessions(endpoint)
    const beforeShells = localShells(await inventory(endpoint))
    expect(beforeShells).toHaveLength(PANES)

    // ── THE RELOAD UNDER TEST: the question is never answered ─────────────
    await armFault(page, 'inventory-always')
    await page.reload()
    await expect(page.locator(TABS)).toHaveCount(PANES, { timeout: 30_000 })
    await expect(page.locator(ATTACH_CARD)).toHaveCount(PANES, { timeout: 30_000 })

    // THE CHOSEN ANSWER TO "THE INVENTORY STAYS UNAVAILABLE": the pane keeps
    // its tab, its scrollback and a retry that names the situation; it does
    // not open a shell it cannot prove is needed, and it does not pretend the
    // session ended. Nothing accumulates while the plane is down.
    expect(await opensRequested(page)).toBe(0)
    expect(await countWindowBudgetRefusals(page)).toBe(0)
    expect(await liveSessions(endpoint)).toHaveLength(PANES)
    expect(localShells(await inventory(endpoint))).toEqual(beforeShells)

    // ── THE PLANE ANSWERS, and the same sessions come back ────────────────
    await setFault(page, 'off')
    await dismissToasts(page)
    await page.locator('.pane.active ' + ATTACH_CARD + ' button').click()
    await expect(page.locator('.pane.active ' + ATTACH_CARD)).toHaveCount(0, {
      timeout: 30_000,
    })
    expect(await opensRequested(page)).toBe(0)
    const afterLive = await liveSessions(endpoint)
    expect(sorted(afterLive.map((s) => s.sessionId))).toEqual(
      sorted(beforeLive.map((s) => s.sessionId)),
    )
    expect(localShells(await inventory(endpoint))).toEqual(beforeShells)
    await runMarker(page, `NOCX-AFTER-OUTAGE-${Date.now().toString(36)}`)
  })

  test('a session the backend proves absent still gets a fresh shell', async ({ page }) => {
    test.setTimeout(180_000)
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await installFaultShim(page)
    await page.goto('/')
    await promptReady(page)
    await expect(page.locator(TABS)).toHaveCount(1, { timeout: 30_000 })

    // The positive control for the whole spec: the recovery rule above must
    // not become "never open a shell". A live entry whose claim the backend
    // answers with `unknown-session` IS an absence, and the pane opens one.
    const live = await liveSessions(endpoint)
    expect(live).toHaveLength(1)
    const shell = localShells(await inventory(endpoint))[0]
    expect(shell).toBeDefined()

    // End the shell through the backend's own authority, leaving the pane's
    // row in the chain: the next restore lists nothing for the pane. This is
    // the EXITED-SESSION case the recovery rule must not swallow.
    await endUnattachedSession(endpoint, live[0])
    await expect.poll(async () => (await liveSessions(endpoint)).length).toBe(0)

    // The session is proven gone by the backend's own answer, which is what
    // `sessions.live` not naming the pane means. Reload: the pane opens a new
    // shell, and the count grows by exactly one.
    await armFault(page, 'off')
    await page.reload()
    await promptReady(page)
    await expect(page.locator(TABS)).toHaveCount(1, { timeout: 30_000 })
    expect(await opensRequested(page)).toBe(1)
    await expect(page.locator(ATTACH_CARD)).toHaveCount(0)
    expect(await liveSessions(endpoint)).toHaveLength(1)
    expect(localShells(await inventory(endpoint))).toHaveLength(1)
    await runMarker(page, `NOCX-FRESH-${Date.now().toString(36)}`)
  })
})
