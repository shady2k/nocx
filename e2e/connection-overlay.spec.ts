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
 *  3. Test 4 cuts a fault proxy while its daemon stays alive, and reads the
 *     terminal recording through session.output because the terminal grid is a
 *     canvas and DOM text cannot prove scrollback identity.
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
import { test as base, expect, type Browser, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { BASE_URL } from './base-url'
import {
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

interface Recording {
  produced: number
  runs: { offset: number; body: string }[]
}

async function oneLiveSession(endpoint: BackendEndpoint): Promise<LiveSession> {
  const result = await controlCall<{ sessions: LiveSession[] }>(endpoint, 'sessions.live', {})
  if (result.sessions.length !== 1) {
    throw new Error(`expected one live session, got ${result.sessions.length}`)
  }
  return result.sessions[0]
}

async function recording(endpoint: BackendEndpoint, live: LiveSession): Promise<Buffer> {
  const result = await controlCall<Recording>(endpoint, 'session.output', {
    sessionId: live.sessionId,
    instanceId: live.instanceId,
    sessionEpoch: live.sessionEpoch,
    from: 0,
  })
  const chunks = result.runs.map((run) => Buffer.from(run.body, 'base64'))
  return Buffer.concat(chunks)
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

function disposableRoot(prefix: string): DisposableRoot {
  return { root: mkdtempSync(join(tmpdir(), prefix)) }
}

test.describe('connection overlay survives backend loss', () => {
  let backend: VaultBackend
  const pages: Page[] = []
  const proxies: FaultProxy[] = []

  test.beforeEach(() => {
    backend = new VaultBackend(readStand().server, disposableRoot('nocx-overlay-'))
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

  test('reconnects through a new port and token across three live cycles', async ({ browser }) => {
    let current = await backend.start()
    const resolved: string[] = []
    const page = await freshClient(browser, async () => {
      resolved.push(`${current.port}:${current.token}`)
      return endpointResolution(current)
    })
    pages.push(page)
    await readyForInput(page)

    let previous = current
    for (let cycle = 0; cycle < 3; cycle += 1) {
      backend.stop()
      await expect(overlay(page)).toBeVisible()
      current = await backend.start()
      const next = current
      expect(next.port).not.toBe(previous.port)
      expect(next.token).not.toBe(previous.token)

      await overlay(page).getByRole('button', { name: 'Retry', exact: true }).click()
      await expect(overlay(page)).toHaveAttribute('data-state', 'online')
      await expect(page.getByRole('button', { name: 'Reconnect', exact: true })).toBeVisible()
      await page.getByRole('button', { name: 'Reconnect', exact: true }).click()
      await readyForInput(page)
      await expect(page.locator('body')).not.toContainText(
        /Invalid params|different backend instance|not connected|ws closed/i,
      )
      expect(resolved).toContain(`${next.port}:${next.token}`)
      previous = next
    }

    await expect(page.locator('#sidebar > .ui-sidebar-view')).toHaveCount(1)
    await expect(page.locator('dialog[open]')).toHaveCount(0)
  })

  test('preserves scrollback across a live-daemon connection cut', async ({ browser }) => {
    const current = await backend.start()
    const proxy = await startFaultProxy('127.0.0.1', current.port)
    proxies.push(proxy)
    const proxiedEndpoint = { port: proxy.port, token: current.token }
    const page = await freshClient(browser, async () => endpointResolution(proxiedEndpoint))
    pages.push(page)
    await readyForInput(page)

    // The marker is assembled so the terminal's echoed command cannot satisfy
    // a recording assertion before the shell actually emits that marker. The
    // large finite loop keeps the daemon producing bytes after the cut without
    // making the test wait on a duration.
    const command =
      "printf 'NOCX_SCROLLBACK_'$(printf BEFORE)'\\n'; i=0; " +
      'while [ "$i" -lt 1000000 ]; do printf \'NOCX_SCROLLBACK_\'$(printf DURING)\'-%06d\\n\' "$i"; ' +
      'i=$((i+1)); done'
    await page.keyboard.type(command)
    await page.keyboard.press('Enter')

    const live = await oneLiveSession(current)
    const beforeMarker = Buffer.from('NOCX_SCROLLBACK_BEFORE')
    const duringMarker = Buffer.from('NOCX_SCROLLBACK_DURING')
    await expect
      .poll(async () => (await recording(current, live)).includes(beforeMarker))
      .toBe(true)
    const before = await recording(current, live)

    // Hold all reconnect attempts in the proxy so the following recording
    // growth is unambiguously produced while the daemon remains alive but the
    // page is disconnected.
    proxy.blackhole()
    proxy.cut()
    await expect(overlay(page)).toBeVisible()
    await expect
      .poll(async () => {
        const output = await recording(current, live)
        return output.length > before.length && output.includes(duringMarker)
      })
      .toBe(true)

    proxy.pass()
    await expect(overlay(page)).toHaveAttribute('data-state', 'online')

    const after = await recording(current, live)
    expect(after.subarray(0, before.length).equals(before)).toBe(true)
    expect(after.includes(duringMarker)).toBe(true)
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
