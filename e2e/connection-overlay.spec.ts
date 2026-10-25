/**
 * THE EPIC'S ACCEPTANCE TEST (nocx-u1qte; design §§5, 7, 8, 10).
 *
 * What a user can do that they could not before: open nocx without a backend,
 * watch it recover when the backend appears or restarts, and continue using the
 * same terminal while a truthful connection surface owns the outage.
 *
 * WHY THIS SPEC OWNS ITS BACKEND. The shared stand is deliberately not touched:
 * it is one backend for the whole Playwright run and other specs depend on its
 * tabs and storage. Each test below starts a VaultBackend in its own disposable
 * home, so stopping and restarting it cannot kill another spec's session or
 * make a green assertion measure somebody else's endpoint.
 *
 * WHY THE RESOLVER IS DYNAMIC. A static bindEndpoint shim would keep returning
 * the first port and token after a restart. This spec exposes a Node-owned
 * callable ResolveBackend implementation instead. Every client attempt asks
 * for the current endpoint, which is the fact criteria 3 and 4 exist to prove.
 *
 * CONDITIONS WITHOUT WHICH A GREEN RUN PROVES NOTHING:
 *
 *  1. Every test uses a genuinely fresh page and the backend this spec started;
 *     no page reload is used to recover a dropped socket.
 *  2. Test 3 asserts both the old and new port/token and repeats the drop/return
 *     cycle three times, then counts the mounted sidebar and open dialogs.
 *  3. Test 4 cuts a fault proxy while its daemon stays alive. Recovery metadata
 *     proves the daemon produced output while disconnected; the integrated
 *     command block's visible output proves the user can read it after recovery.
 *  4. Test 6 supplies each exact FailureKind message/remedy from the backend
 *     source. The server-binary remedy is platform-specific; this run is Linux.
 *  5. Test 7 opens the real vault unlock surface before stopping the backend and
 *     proves the overlay becomes the remaining interactive surface.
 *  6. Test 6 supplies failures through the resolver shim, so it proves the
 *     overlay renders the supplied message and remedy, not that Go's strings
 *     reach the page. `main_test.go` (`TestResolveBackendReportsSuccessAndFailure`)
 *     holds that transport half; these literals mirror launcher.go so drift is
 *     found by grep.
 *
 * No duration is used as an assertion. Every wait below observes a DOM state,
 * visibility, prompt readiness, or backend recording state.
 */
import { expect, type Browser, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { BASE_URL } from './base-url'
import {
  standalone as base,
  VaultBackend,
  bindResolvableEndpoint,
  clickIntoEditor,
  openControlPlane,
  promptReady,
  settingsReady,
  type BackendEndpoint,
  type DisposableRoot,
} from './harness'
import { startFaultProxy, type FaultProxy } from './fault-proxy'
import { readStand } from './stand'

const test = base

test.describe.configure({ timeout: 240_000 })

type Resolution =
  | {
      ok: true
      host: string
      port: number
      token: string
      kind: ''
      message: ''
      remedy: ''
    }
  | { ok: false; kind: string; message: string; remedy: string }

type Resolver = () => Promise<Resolution>

const NOT_READY_FAILURE: Resolution = {
  ok: false,
  kind: 'not-ready',
  message: 'The nocx backend could not be reached.',
  remedy:
    'The backend could not start or respond. If retrying does not help, check whether another instance is already running and look for the backend’s startup error.',
}

function endpointResolution(endpoint: BackendEndpoint): Resolution {
  return {
    ok: true,
    host: '127.0.0.1',
    port: endpoint.port,
    token: endpoint.token,
    kind: '',
    message: '',
    remedy: '',
  }
}

async function freshClient(browser: Browser, resolver: Resolver): Promise<Page> {
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await bindResolvableEndpoint(page, resolver)
  await page.goto('/')
  return page
}

async function readyForInput(page: Page): Promise<void> {
  await clickIntoEditor(page)
  await promptReady(page)
}

function overlay(page: Page) {
  return page.locator('.ui-connection-overlay')
}

async function controlCall<T>(
  endpoint: BackendEndpoint,
  method: string,
  params: unknown,
): Promise<T> {
  const wire = await openControlPlane(endpoint.port, endpoint.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

interface LiveSession {
  sessionId: string
  instanceId: string
  sessionEpoch: number
  paneId: string | null
}

async function oneLiveSession(endpoint: BackendEndpoint): Promise<LiveSession> {
  const result = await controlCall<{ sessions: LiveSession[] }>(endpoint, 'sessions.live', {})
  if (result.sessions.length !== 1) {
    throw new Error(`expected one live session, got ${result.sessions.length}`)
  }
  return result.sessions[0]
}

async function recoveryFrontier(endpoint: BackendEndpoint, live: LiveSession): Promise<number> {
  const status = await controlCall<{ produced: number }>(endpoint, 'session.recoveryStatus', {
    sessionId: live.sessionId,
    instanceId: live.instanceId,
    sessionEpoch: live.sessionEpoch,
  })
  return status.produced
}

async function liveScreenText(page: Page): Promise<string> {
  return (await page.locator('.pane.active .terminal-live-grid').textContent()) ?? ''
}

const FAILURE_CASES = [
  {
    name: 'profile-unusable',
    message:
      'The profile runtime directory /tmp/nocx-overlay-profile could not be created or used.',
    remedy: 'Check that /tmp/nocx-overlay-profile can be read and written, then retry.',
  },
  {
    name: 'server-binary-unusable',
    message: 'The nocx server binary could not be started.',
    // Platform-specific source text: the container runs this case on Linux.
    remedy: 'Repair the nocx server binary under ~/.local/share/nocx/bin, then retry.',
  },
  {
    name: 'incompatible-coordinator',
    message: 'A coordinator at /tmp/nocx-overlay.sock answered with an incompatible response.',
    remedy: 'Stop the other or older nocx coordinator, then retry.',
  },
  {
    name: 'not-ready',
    message: 'The nocx backend could not be reached.',
    remedy:
      'The backend could not start or respond. If retrying does not help, check whether another instance is already running and look for the backend’s startup error.',
  },
] as const

/** The pids of this machine's helper daemons started under one home. */
function daemonsFor(isolatedHome: string): number[] {
  try {
    return execFileSync('pgrep', ['-f', join(isolatedHome, '.nocx', 'helper')], {
      encoding: 'utf8',
    })
      .split('\n')
      .map((line) => Number(line.trim()))
      .filter((pid) => Number.isInteger(pid) && pid > 0)
  } catch {
    // pgrep exits non-zero when nothing matches, which is an answer.
    return []
  }
}

/**
 * Stop the helper daemon that owns one home's shells, and WAIT for it to go on
 * the OS's own answer rather than on a duration.
 *
 * The daemon is detached by design (D1) and outlives the coordinator — which is
 * exactly what the three cycles above rely on — so this is the only way a spec
 * can end a helper-hosted session FOR REAL. Same two steps, same helpers and
 * same reason as e2e/local-session-reattach.spec.ts, which is where they were
 * bought for the teardown side of the same problem.
 */
async function endHelperDaemonsUnder(isolatedHome: string): Promise<void> {
  for (const signal of ['SIGTERM', 'SIGKILL'] as const) {
    const survivors = daemonsFor(isolatedHome)
    if (survivors.length === 0) return
    for (const pid of survivors) {
      try {
        process.kill(pid, signal)
      } catch {
        /* already gone */
      }
    }
    const deadline = Date.now() + 15_000
    while (Date.now() < deadline) {
      if (daemonsFor(isolatedHome).length === 0) return
      const { promise, resolve: resume } = Promise.withResolvers<void>()
      setTimeout(resume, 50)
      await promise
    }
  }
  throw new Error(
    `the helper daemon(s) ${daemonsFor(isolatedHome).join(', ')} outlived SIGTERM and SIGKILL`,
  )
}

function disposableRoot(prefix: string): DisposableRoot {
  return { root: mkdtempSync(join(tmpdir(), prefix)) }
}

test.describe('connection overlay survives backend loss', () => {
  let backend: VaultBackend
  let backendRoot: DisposableRoot
  const pages: Page[] = []
  const proxies: FaultProxy[] = []

  test.beforeEach(() => {
    backendRoot = disposableRoot('nocx-overlay-')
    backend = new VaultBackend(readStand().server, backendRoot)
  })

  test.afterEach(async () => {
    for (const page of pages.splice(0)) await page.context().close()
    for (const proxy of proxies.splice(0)) await proxy.close()
    backend?.stop()
  })

  test('opens without a backend, then becomes usable when one appears', async ({ browser }) => {
    let current: BackendEndpoint | null = null
    const page = await freshClient(browser, async () =>
      current === null ? NOT_READY_FAILURE : endpointResolution(current),
    )
    pages.push(page)

    // The first observable frame is already the connection surface; no backend
    // has started yet, so this cannot be a late error from a failed tab.
    await expect(overlay(page)).toBeVisible()
    await expect(overlay(page)).toHaveAttribute('data-state', 'blocked')

    current = await backend.start()
    await overlay(page).getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(overlay(page)).toHaveAttribute('data-state', 'online')
    await expect(overlay(page)).not.toBeVisible()
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await readyForInput(page)
  })

  test('shows the outage without leaking ws errors into the document', async ({ browser }) => {
    const current = await backend.start()
    const page = await freshClient(browser, async () => endpointResolution(current))
    pages.push(page)
    await readyForInput(page)

    backend.stop()
    await expect(overlay(page)).toBeVisible()
    await expect(overlay(page)).toHaveAttribute('data-state', /^(connecting|waiting)$/)
    await expect(page.locator('body')).not.toContainText(/ws closed|not connected/i)
  })

  test('reconnects through a new port and token across three live cycles, and offers Reconnect only when the session is really gone', async ({
    browser,
  }) => {
    let current = await backend.start()
    // OFF for exactly as long as this test wants it off, ON only once this
    // test has itself decided to retry. The dispatcher's own automatic
    // reconnect (backoff timer, independent of any click) can otherwise beat
    // a scripted Retry click to a fast local restart — correct product
    // behaviour (nothing should wait on a person's click), but a race this
    // spec cannot depend on winning (AGENTS.md: "a test may not depend on
    // timing"). `blocked` is what makes armed=false airtight rather than
    // merely unlikely to lose: unlike `waiting`, it schedules no automatic
    // retry of its own (Dispatcher._attemptConnection returns right after
    // `setConnectionState({kind:'blocked', ...})`, with no call to
    // `_scheduleReconnect`), so with the gate off there is no timer left
    // that could ever win — the only way another resolve() call happens is
    // this test's own Retry click. This is the fix for the alternating
    // connection-overlay.spec.ts:237/:324 failures (nocx-oez54, nocx-61k0g):
    // the race was in the spec, not in the overlay's state machine.
    let armed = true
    const resolved: string[] = []
    const page = await freshClient(browser, async () => {
      if (!armed) return NOT_READY_FAILURE
      resolved.push(`${current.port}:${current.token}`)
      return endpointResolution(current)
    })
    pages.push(page)
    await readyForInput(page)

    // THE SHELL THIS PANE IS THE PIPE OF, as the BACKEND holds it. The three
    // cycles below must keep it (nocx-xn63t.6.33): the coordinator that comes
    // back is a new backend INSTANCE, but the helper under this home owns the
    // shell and the replacement re-adopts the session under its own identity,
    // so the pane has lost nothing to reopen.
    const first = await oneLiveSession(current)
    const sessionId = first.sessionId
    const paneId = first.paneId

    let previous = current
    for (let cycle = 0; cycle < 3; cycle += 1) {
      armed = false
      backend.stop()
      await expect(overlay(page)).toBeVisible()
      current = await backend.start()
      const next = current
      expect(next.port).not.toBe(previous.port)
      expect(next.token).not.toBe(previous.token)

      armed = true
      await overlay(page).getByRole('button', { name: 'Retry', exact: true }).click()
      await expect(overlay(page)).toHaveAttribute('data-state', 'online')

      // THE SESSION CAME BACK, SO THE PANE MUST NOT OFFER TO REPLACE IT.
      // "Reconnecting opens a NEW shell" over a shell that is still running is
      // the worst sentence this surface could make — the owner saw it over two
      // live panes on 2026-10-17, and pressing it would have abandoned a
      // running agent. So this is not merely "the offer is absent"; the pane
      // still holds the session it had, and the backend's own answer says so:
      // the same sessionId, the same paneId, re-adopted by the coordinator
      // that just started.
      await expect(page.getByRole('button', { name: 'Reconnect', exact: true })).toHaveCount(0)
      await expect(page.locator('body')).not.toContainText('The connection is gone')
      const live = await oneLiveSession(next)
      expect(live.sessionId).toBe(sessionId)
      expect(live.paneId).toBe(paneId)
      // And the editor is the pane's own, never replaced by an offer: this
      // succeeds with no Reconnect press in between, which is the whole
      // difference from a lost pane.
      await readyForInput(page)
      // THE PANE LEAKS NOTHING ABOUT THE CLAIM ITSELF. This assertion used to
      // read the whole DOCUMENT, and it passed while a lost pane's Reconnect
      // opened a NEW session — a new session is a new Files scope, which
      // re-opened the sidebar's binding as a side effect. A pane that KEEPS its
      // session re-scopes nothing, so the same restart now leaves the sidebar's
      // own stale binding on screen: "Invalid params: unknown bindingId" and
      // its Retry button, which is the Files panel's sticky watch-failure
      // message BY DESIGN (frontend/src/files/files-store.ts: a rejected
      // files.watch stays until a successful one, and the header refresh is the
      // retry). That is a separate defect in the panel's binding lifecycle —
      // the binding is minted in the coordinator's registry, so a restart
      // leaves every id the panel holds dead, and nothing but an origin change
      // re-opens it — reported with this change rather than papered over here.
      // Scoping the assertion to the surface this spec is about keeps exactly
      // the claim it was written to make: no refusal of the reconnect reaches
      // the pane.
      await expect(page.locator('.pane.active')).not.toContainText(
        /Invalid params|different backend instance|not connected|ws closed/i,
      )
      expect(resolved).toContain(`${next.port}:${next.token}`)
      previous = next
    }

    // THE OTHER HALF OF THAT DISTINCTION, and the reason the three cycles
    // above are not asserting "the offer never appears". A session that really
    // IS gone must still be offered Reconnect. What holds this pane's shell is
    // the helper daemon under this home — it is detached by design (D1) and
    // outlives the coordinator — so stopping it ends the session for real:
    // the coordinator that then starts has nothing to re-adopt, the page's
    // re-claim finds no live entry, and only then is the pane marked lost.
    // (The same offer, for a host loss rather than a local one, is what
    // e2e/ssh-reconnect.spec.ts asserts and clicks.)
    await endHelperDaemonsUnder(backend.isolatedHome)
    armed = false
    backend.stop()
    await expect(overlay(page)).toBeVisible()
    current = await backend.start()
    armed = true
    await overlay(page).getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(overlay(page)).toHaveAttribute('data-state', 'online')
    await expect(page.getByRole('button', { name: 'Reconnect', exact: true })).toBeVisible()

    await expect(page.locator('#sidebar > .ui-sidebar-view')).toHaveCount(1)
    await expect(page.locator('dialog[open]')).toHaveCount(0)
  })

  test('preserves scrollback across a live-daemon connection cut', async ({
    browser,
    browserName,
  }) => {
    // QUARANTINED on webkit, tracked by nocx-61k0g (P1), which is blocked by
    // nocx-n14oo.11. About one run in five the overlay's Retry button resolves
    // and stays not-visible until the test times out. Two hypotheses were
    // measured and ruled out on 2026-09-16: a missing deadline on a connecting
    // attempt (fixed in b2a44bfd, :324 is green) and the dispatcher's backoff
    // timer winning the click (still 4 of 5 with page.clock installed). The
    // Playwright call log cannot say which state the overlay is in or why, so
    // the next step is the per-test context a failure prints, not another guess.
    test.fixme(
      browserName === 'webkit',
      'nocx-61k0g: Retry stays not-visible about one run in five',
    )
    const current = await backend.start()
    const proxy = await startFaultProxy('127.0.0.1', current.port)
    proxies.push(proxy)
    const proxiedEndpoint = { port: proxy.port, token: current.token }
    const page = await freshClient(browser, async () => endpointResolution(proxiedEndpoint))
    pages.push(page)
    await readyForInput(page)

    // Keep the first marker in retained history while the daemon continues
    // producing only after the proxy has cut the page off. The release file
    // lives under this test's disposable backend root, so no shared state or
    // timing guess controls the producer.
    const releaseGate = join(backendRoot.root, 'release-scrollback-producer')
    const command =
      `printf 'NOCX_SCROLLBACK_'$(printf BEFORE)'\\n'; while [ ! -f '${releaseGate}' ]; do sleep 0.05; done; ` +
      `i=1; while [ "$i" -le 40 ]; do printf 'NOCX_SCROLLBACK_DURING-%03d\n' "$i"; i=$((i+1)); done; ` +
      `printf 'NOCX_SCROLLBACK_'$(printf DONE)'\\n'`
    await page.keyboard.type(command)
    await page.keyboard.press('Enter')

    const live = await oneLiveSession(current)
    await expect
      .poll(async () => liveScreenText(page), { timeout: 20_000 })
      .toContain('NOCX_SCROLLBACK_BEFORE')
    const before = await recoveryFrontier(current, live)

    // Hold all reconnect attempts in the proxy while the shell waits at the
    // gate. Releasing it now produces a bounded, known output burst while the
    // page is disconnected; the recovery frontier proves the daemon received
    // that output before the proxy is opened again.
    proxy.blackhole()
    proxy.cut()
    await expect(overlay(page)).toBeVisible()
    writeFileSync(releaseGate, 'release\n')
    const expectedBurst =
      Array.from(
        { length: 40 },
        (_, index) => `NOCX_SCROLLBACK_DURING-${String(index + 1).padStart(3, '0')}\n`,
      ).join('') + 'NOCX_SCROLLBACK_DONE\n'
    await expect
      .poll(async () => (await recoveryFrontier(current, live)) - before, { timeout: 30_000 })
      .toBeGreaterThanOrEqual(expectedBurst.length)

    proxy.pass()
    await expect(overlay(page)).toHaveAttribute('data-state', 'online')
    await expect
      .poll(async () => liveScreenText(page), { timeout: 30_000 })
      .toContain('NOCX_SCROLLBACK_DONE')

    // Reconnection can invalidate the command's coordinate qualification.
    // Read the currently painted output, whether it belongs to the command,
    // the whole-terminal history projection, or the live grid. The markers
    // are deliberately absent from the echoed command, so it cannot satisfy
    // these checks; the overlapping, hidden archive cannot satisfy them either.
    const commandBlock = page
      .locator('.pane.active .scrollback-inner > .cmd-block[data-block-kind="command"]')
      .filter({ hasText: 'release-scrollback-producer' })
      .last()
    const entryId = await commandBlock.getAttribute('data-entry-id')
    expect(entryId).toBeTruthy()
    const scrollback = page.locator('.pane.active .scrollback-area')
    const outputRows =
      '.live-history .term-grid-row, .terminal-live-grid .term-grid-row, .cmd-output .term-line, .cmd-output .term-grid-row'
    const visibleOutputRows = async (): Promise<string[]> =>
      page.evaluate(
        ({ id, selector }) => {
          const area = document.querySelector('.pane.active .scrollback-area')
          const block = document.querySelector(
            `.pane.active .scrollback-inner > .cmd-block[data-entry-id="${id}"]`,
          )
          if (!(area instanceof HTMLElement) || !(block instanceof HTMLElement)) {
            throw new Error('scrollback area or command block is missing')
          }
          const clip = area.getBoundingClientRect()
          return Array.from(area.querySelectorAll<HTMLElement>(selector))
            .filter((row) => {
              const bounds = row.getBoundingClientRect()
              // Match the live-row probe's clipping rule while keeping all
              // metrics and extracted text in this one DOM snapshot.
              let top = clip.top
              let bottom = clip.bottom
              for (let node = row.parentElement; node; node = node.parentElement) {
                if (getComputedStyle(node).overflowY !== 'visible') {
                  const ancestor = node.getBoundingClientRect()
                  top = Math.max(top, ancestor.top)
                  bottom = Math.min(bottom, ancestor.bottom)
                }
              }
              return (
                row.checkVisibility() &&
                bounds.height > 0 &&
                bounds.bottom > top + 0.5 &&
                bounds.top < bottom - 0.5
              )
            })
            .map((row) => (row.textContent ?? '').trim())
        },
        { id: entryId!, selector: outputRows },
      )
    const scrollUntilOutputShows = async (marker: string, deltaY: number): Promise<void> => {
      await scrollback.hover()
      try {
        await expect
          .poll(
            async () => {
              if ((await visibleOutputRows()).some((row) => row.includes(marker))) return true
              await page.mouse.wheel(0, deltaY)
              return false
            },
            { timeout: 30_000, intervals: [250] },
          )
          .toBe(true)
      } catch (error) {
        const diagnostic = await page.evaluate(
          ({ id, selector }) => {
            const area = document.querySelector('.pane.active .scrollback-area')
            const block = document.querySelector(
              `.pane.active .scrollback-inner > .cmd-block[data-entry-id="${id}"]`,
            )
            const output = block?.querySelector<HTMLElement>('.cmd-output')
            const clip = area?.getBoundingClientRect()
            return {
              outputText: (output?.textContent ?? '').slice(0, 800),
              outputTextLength: output?.textContent?.length ?? 0,
              outputClass: output?.className ?? null,
              outputVisible: output?.checkVisibility() ?? false,
              areaScrollTop: area instanceof HTMLElement ? area.scrollTop : null,
              areaClientHeight: area instanceof HTMLElement ? area.clientHeight : null,
              areaScrollHeight: area instanceof HTMLElement ? area.scrollHeight : null,
              areaBounds: clip ? { top: clip.top, bottom: clip.bottom } : null,
              rows: Array.from(area?.querySelectorAll<HTMLElement>(selector) ?? []).map((row) => {
                const bounds = row.getBoundingClientRect()
                return {
                  className: row.className,
                  text: (row.textContent ?? '').slice(0, 100),
                  top: bounds.top,
                  painted: row.checkVisibility(),
                  bottom: bounds.bottom,
                  visible:
                    clip !== undefined && bounds.bottom > clip.top && bounds.top < clip.bottom,
                }
              }),
            }
          },
          { id: entryId!, selector: outputRows },
        )
        throw new Error(
          `scrollback output ${marker} not visible: ${JSON.stringify(diagnostic)}; ${String(error)}`,
        )
      }
    }
    await scrollUntilOutputShows('NOCX_SCROLLBACK_BEFORE', -500)
    await scrollUntilOutputShows('NOCX_SCROLLBACK_DURING-001', -500)
    await scrollUntilOutputShows('NOCX_SCROLLBACK_DONE', 500)

    // A real wheel gesture returns the reader to the live end before checking
    // the command's final row on the current screen.
    await expect
      .poll(
        async () => {
          const atEnd = await page.evaluate(() => {
            const area = document.querySelector('.pane.active .scrollback-area')
            return (
              area instanceof HTMLElement &&
              area.scrollTop + area.clientHeight >= area.scrollHeight - 2
            )
          })
          if (atEnd) return true
          await page.mouse.wheel(0, 1400)
          return false
        },
        { timeout: 30_000, intervals: [250] },
      )
      .toBe(true)
    await expect
      .poll(async () => liveScreenText(page), { timeout: 20_000 })
      .toContain('NOCX_SCROLLBACK_DONE')

    await expect(page.locator('#sidebar > .ui-sidebar-view')).toHaveCount(1)
    await expect(page.getByRole('toolbar', { name: 'Activity bar' })).toBeVisible()
    await expect(page.locator('.ui-sidebar-view__header')).toBeVisible()
  })

  test('makes Retry act in waiting and hides it while connecting', async ({ browser }) => {
    let current = await backend.start()
    let attempt = 0
    let release: (() => void) | undefined
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    const page = await freshClient(browser, async () => {
      attempt += 1
      if (attempt === 1) return endpointResolution(current)
      if (attempt === 2) return endpointResolution(current)
      await held
      return endpointResolution(current)
    })
    pages.push(page)
    await readyForInput(page)

    backend.stop()
    await expect(overlay(page)).toHaveAttribute('data-state', 'waiting')
    const retry = overlay(page).getByRole('button', { name: 'Retry', exact: true })
    await expect(retry).toHaveCount(1)

    current = await backend.start()
    await retry.click()
    await expect(overlay(page)).toHaveAttribute('data-state', 'connecting')
    await expect(overlay(page).getByRole('button', { name: 'Retry', exact: true })).toHaveCount(0)
    release!()
    await expect(overlay(page)).toHaveAttribute('data-state', 'online')
  })

  for (const failure of FAILURE_CASES) {
    test(`keeps the window open and explains ${failure.name}`, async ({ browser }) => {
      const current = await backend.start()
      const page = await freshClient(browser, async () => ({
        ok: false,
        kind: failure.name,
        ...failure,
      }))
      pages.push(page)

      await expect(overlay(page)).toHaveAttribute('data-state', 'blocked')
      await expect(overlay(page).locator('.ui-connection-overlay__headline')).toHaveText(
        failure.message,
      )
      await expect(overlay(page).locator('.ui-connection-overlay__detail')).toHaveText(
        failure.remedy,
      )
      await expect(page.getByRole('button', { name: 'Retry', exact: true })).toBeVisible()
      expect(backend.running).toBe(true)
      void current
    })
  }

  test('closes the vault dialog on disconnect and leaves Retry interactive', async ({
    browser,
  }) => {
    const current = await backend.start()
    const page = await freshClient(browser, async () => endpointResolution(current))
    pages.push(page)

    await controlCall(current, 'vault.setup', { passphrase: 'overlay-passphrase-42' })
    await controlCall(current, 'vault.seal', {})

    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="secrets"]').click()
    await expect(page.getByRole('button', { name: 'Unlock vault', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Unlock vault', exact: true }).click()

    const unlock = page
      .getByRole('dialog')
      .filter({ has: page.getByRole('button', { name: 'Unlock', exact: true }) })
    await expect(unlock).toBeVisible()

    backend.stop()
    await expect(unlock).not.toBeVisible()
    await expect(overlay(page)).toBeVisible()
    await expect(overlay(page).getByRole('button', { name: 'Retry', exact: true })).toBeVisible()
  })
})
