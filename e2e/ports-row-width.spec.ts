// Ports row width at the default rail (nocx-4wbx) — the defect this exists
// to catch is INVISIBLE to jsdom: jsdom computes no layout, so a row that
// visibly truncates satisfies every DOM-text and stylesheet-contract
// assertion. That is how it shipped twice. This spec measures the real
// layout in a real browser: the destination element's scrollWidth against
// its clientWidth, with the pointer NOT over the row.
//
// The arithmetic the defect rests on, verified in the brief: at
// SIDEBAR_WIDTH_DEFAULT = 240, `.ui-icon-button[data-size='xs']` = 18px and
// `.ports-row__main`'s gap = 8px. `opacity: 0` does not remove a flex item
// from layout, so a forwarded row's THREE hidden buttons (Copy, Open, Stop)
// reserved ~70px of the text column — ~52px more than a plain detected
// row's single button — and the destination line truncated while the
// pointer was elsewhere.
//
// Row fixtures come from the REAL app with no shared-infra changes:
//
// - The PLAIN detected row is the local tab's own listener: the initial tab
//   is a local shell, local discovery reads the kernel (internal/nativeports,
//   no probe binary), and a `node` listener started in that shell shows up
//   as a detected row.
// - The FORWARDED row is a profile-level REMOTE forward replayed at
//   connection open against cmd/e2e-sshd, which implements `tcpip-forward`
//   (the fixture deliberately rejects `direct-tcpip`, so the panel's local
//   Forward button cannot be used inside this container; the orphan
//   forwarded row renders the same three-button row the fix is about).
//
// The remote-forward destination is the brief's own failing example
// (`192.168.0.93:9993`), the string the bug report shows truncating.
//
// W7 revision (nocx-4wbx): CI webkit once failed this spec before the
// measurement — the setup produced TWO rows for the same destination (a
// stale failed one and the live one) and the forwarded-row locator was
// strict-mode ambiguous. The product now supersedes that shape: a running
// forward hides an earlier failure for the same destination. The locator
// below is additionally scoped by data-state, so the stale failed row can
// never satisfy it. In this scene at most one RUNNING row can exist — the
// profile carries a single forward, and a second replay of it would fail
// its bind rather than mint a second running record.
//
import { test, expect, promptReady } from './harness'
import { readStand } from './stand'
import { spawn, execFileSync } from 'node:child_process'
import { createServer, connect } from 'node:net'
import { mkdirSync, writeFileSync, existsSync } from 'node:fs'
import path from 'node:path'
import type { ChildProcess } from 'node:child_process'
import type { Locator, Page } from '@playwright/test'

const VIEW_PORTS = 'button[data-view="ports"]'
const DETECTED_ROW = '[data-testid="detected-row"]'
const ACTIONS = '.ports-row__actions'
const ADDR = '.ports-row__addr'
const DEST = '.ports-row__dest'

/** The remote-forward destination the brief's bug report shows truncating. */
const LONG_DESTINATION = '192.168.0.93:9993'

interface Fixture {
  proc: ChildProcess
  addr: string
  userKey: string
  knownHosts: string
}

/** Build (once per run) and spawn the in-process sshd; read its handshake.
 *  The fixture is the same one shell-mode.spec.ts and nocxify-journey.spec.ts
 *  drive (a fixture may be shared by copying its pattern; it may not be
 *  dishonest about the protocol — cmd/e2e-sshd implements tcpip-forward). */
function startSshd(): Fixture & { _wait: Promise<void> } {
  const bin = path.resolve(
    process.env.TMPDIR ?? '/tmp',
    `nocx-e2e-sshd-${process.pid}-${Date.now()}`,
  )
  if (!existsSync(bin)) {
    execFileSync('go', ['build', '-o', bin, './cmd/e2e-sshd'], {
      cwd: path.resolve(__dirname, '..'),
    })
  }
  // The fixture's HOME is the REMOTE host's home, separate from the backend's
  // (nocx-z9s9.8): without it the connection would come up already integrated.
  const remoteHome = path.join(path.dirname(readStand().home), 'row-width-remote-home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  const proc = spawn(bin, [], {
    stdio: ['ignore', 'pipe', 'inherit'],
    env: {
      ...process.env,
      HOME: remoteHome,
      ZDOTDIR: remoteHome,
      XDG_CONFIG_HOME: path.join(remoteHome, '.config'),
      XDG_DATA_HOME: path.join(remoteHome, '.local', 'share'),
      XDG_CACHE_HOME: path.join(remoteHome, '.cache'),
    },
  })
  const lines: string[] = []
  let addr = ''
  let userKey = ''
  let knownHosts = ''
  const deadline = Date.now() + 15_000
  const reader = new Promise<void>((resolve, reject) => {
    proc.stdout?.on('data', (chunk: Buffer) => {
      for (const line of chunk.toString().split('\n')) {
        const trimmed = line.trim()
        if (!trimmed) continue
        lines.push(trimmed)
        if (trimmed.startsWith('ADDR=')) addr = trimmed.slice(5)
        if (trimmed.startsWith('USERKEY=')) userKey = trimmed.slice(8)
        if (trimmed.startsWith('KNOWNHOSTS=')) knownHosts = trimmed.slice(11)
        if (trimmed === 'READY') resolve()
      }
      if (Date.now() > deadline)
        reject(new Error(`e2e-sshd did not print READY: ${lines.join('|')}`))
    })
    proc.on('exit', (code) =>
      reject(new Error(`e2e-sshd exited early (${code}): ${lines.join('|')}`)),
    )
  })
  return {
    proc,
    get addr() {
      return addr
    },
    get userKey() {
      return userKey
    },
    get knownHosts() {
      return knownHosts
    },
    _wait: reader,
  } as Fixture & { _wait: Promise<void> }
}

/** Seed the isolated home's known_hosts so the backend's ssh client accepts
 *  the fixture's host key (the devharness runs with that HOME). */
function trustHostKey(fixture: Fixture): void {
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
}

/** Call one JSON-RPC method over the real backend socket, as the app does. */
async function rpc<T>(
  page: Page,
  port: number,
  token: string,
  method: string,
  params: unknown,
): Promise<T> {
  return page.evaluate(
    ({ port, token, method, params }) =>
      new Promise<T>((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, [`nocx.token.${token}`])
        const timer = setTimeout(() => reject(new Error(`rpc ${method} timed out`)), 10_000)
        ws.onopen = () => {
          ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }))
        }
        ws.onmessage = (ev: MessageEvent) => {
          const msg = JSON.parse(String(ev.data)) as { result?: T; error?: { message?: string } }
          clearTimeout(timer)
          ws.close()
          if (msg.error) reject(new Error(`${method}: ${msg.error.message ?? 'rpc error'}`))
          else resolve(msg.result as T)
        }
        ws.onerror = () => {
          clearTimeout(timer)
          reject(new Error(`${method}: websocket error`))
        }
      }),
    { port, token, method, params },
  )
}

/** The backend port/token through the (stubbed) wails bindings. */
async function wsInfo(page: Page): Promise<{ port: number; token: string }> {
  return page.evaluate(async () => {
    const w = window as unknown as Record<string, unknown>
    const main = (w.go as Record<string, unknown>).main as Record<string, unknown>
    const app = main.WailsApp as {
      GetWSPort: () => Promise<number>
      GetWSToken: () => Promise<string>
    }
    return { port: await app.GetWSPort(), token: await app.GetWSToken() }
  })
}

/** The measurement the spec is about: does the element's content overflow its
 *  box? scrollWidth > clientWidth means the ellipsis is in play — the line
 *  does not fit. Asserted numerically, never via the rendered text. */
const measure = (el: Locator): Promise<{ sw: number; cw: number }> =>
  el.evaluate((node) => {
    const n = node as HTMLElement
    return { sw: n.scrollWidth, cw: n.clientWidth }
  })

/** The pointer must be over nothing: parked on the activity bar, whose
 *  leftmost 48px column the sidebar panel never overlaps. */
async function pointerAway(page: Page): Promise<void> {
  await page.mouse.move(2, 2)
}

/** Ask the kernel for a port nothing is using, rather than guessing one.
 *
 *  The guess was `38200 + rand(100)`, and its comment said random avoided a
 *  collision with a leaked listener from a retried run. It does the opposite:
 *  a hundred candidates is a hundred ways to land on a port something already
 *  holds, and `listen()` then fails, `node` exits, and the spec waits out its
 *  timeout on a row that was never going to exist. A port the kernel just
 *  handed out is free by construction. */
async function reserveFreePort(): Promise<number> {
  const probe = createServer()
  try {
    await new Promise<void>((resolve, reject) => {
      probe.once('error', reject)
      probe.listen(0, '0.0.0.0', resolve)
    })
    const address = probe.address()
    if (typeof address !== 'object' || address === null) {
      throw new Error('the probe socket reported no port')
    }
    return address.port
  } finally {
    await new Promise<void>((resolve) => probe.close(() => resolve()))
  }
}

/** Can something be reached on this port? A yes/no observation, not a wait. */
function isListening(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = connect({ port, host: '127.0.0.1' })
    const settle = (answer: boolean) => {
      socket.destroy()
      resolve(answer)
    }
    socket.setTimeout(1_000)
    socket.once('connect', () => settle(true))
    socket.once('timeout', () => settle(false))
    socket.once('error', () => settle(false))
  })
}

/** The rail must be at its default width for the measurement to mean what
 *  the bug is about. The fresh disposable home has no persisted width, so
 *  240 is guaranteed; the assertion makes that a fact rather than a hope. */
async function expectDefaultRail(page: Page): Promise<void> {
  const width = await page.locator('#sidebar').evaluate((el) => el.getBoundingClientRect().width)
  expect(width).toBeGreaterThanOrEqual(230)
  expect(width).toBeLessThanOrEqual(250)
}

test('a plain detected row keeps its address readable at the default rail width, pointer away', async ({
  page,
}) => {
  test.setTimeout(90_000)
  await page.goto('/')
  await promptReady(page)

  // A listener in the LOCAL shell (the initial tab is this machine's shell,
  // and local discovery reads the kernel — no probe binary involved).
  const port = await reserveFreePort()
  await page.keyboard.type(`node -e 'require("net").createServer().listen(${port},"0.0.0.0")' &`)
  await page.keyboard.press('Enter')

  // The listener must be UP before the panel can be blamed for not showing it.
  // Without this the spec waited thirty seconds on a row whose absence could
  // mean the port was taken, the keystrokes were dropped, or `node` never ran
  // — and reported all three as "element(s) not found", which is what it did
  // on CI webkit. The test process shares this container's network namespace,
  // so connecting to the port is a direct observation, not a proxy for one.
  await expect
    .poll(() => isListening(port), {
      message:
        `nothing is listening on ${port} — the shell never started the ` +
        `listener, so this run cannot say anything about the ports panel`,
      timeout: 20_000,
    })
    .toBe(true)

  // Open the Ports view and wait for the row that owns the listener. From here
  // a timeout IS the product's fault, which is the only reason to wait at all.
  await page.locator(VIEW_PORTS).click()
  const row = page.locator(DETECTED_ROW, { hasText: String(port) })
  await expect(row).toBeVisible({ timeout: 30_000 })
  await expectDefaultRail(page)

  // The measurement: pointer NOT over the row, so the hidden actions are
  // hidden — the state the bug report is about. The address must fit. The
  // numbers are logged so a run's report carries evidence, not adjectives.
  await pointerAway(page)
  const addr = await measure(row.locator(ADDR))
  console.log(`detected addr sw=${addr.sw} cw=${addr.cw}`)
  expect(addr.sw, `address overflows (${addr.sw} > ${addr.cw})`).toBeLessThanOrEqual(addr.cw)
  // The measurement above was taken with the actions hidden — the pointer is
  // away from the row, which is the state the bug report is about.
  await expect(row.locator(ACTIONS)).toHaveCSS('opacity', '0')

  // Pointer reachability: pointing at the row reveals the action group.
  await row.hover()
  await expect(row.locator(ACTIONS)).toHaveCSS('opacity', '1')

  // Keyboard reachability: focusing an action (no hover) reveals it too —
  // the :focus-within path the reveal exists for.
  await pointerAway(page)
  await row.locator('[data-testid="ports-copy"]').focus()
  await expect(row.locator(ACTIONS)).toHaveCSS('opacity', '1')
})

test('a forwarded row keeps its destination readable at the default rail width, pointer away', async ({
  page,
}) => {
  test.setTimeout(120_000)
  const fixture = startSshd()
  let createdId: string | null = null
  try {
    await (fixture as Fixture & { _wait: Promise<void> })._wait
    expect(fixture.addr).not.toBe('')
    trustHostKey(fixture)

    await page.goto('/')
    const info = await wsInfo(page)

    // A profile whose stored REMOTE forward replays at connection open. The
    // fixture implements tcpip-forward (the -R direction); it deliberately
    // refuses direct-tcpip, so this is the one way a real forwarded row can
    // exist in this container without shared-infra changes. The destination
    // is the brief's failing example — the string the bug report shows
    // truncating on exactly this three-button row.
    const profileName = `e2e-row-width-${Date.now()}`
    const created = await rpc<{ id: string }>(page, info.port, info.token, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.addr.split(':')[0],
        port: Number(fixture.addr.split(':')[1]),
        user: 'e2e',
        keyPath: fixture.userKey,
        forwards: [
          {
            direction: 'remote',
            bindHost: '127.0.0.1',
            bindPort: 39871,
            destination: LONG_DESTINATION,
          },
        ],
      },
    })
    createdId = created.id

    // Open the connection through quick connect, the user path.
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    await page.keyboard.press('Enter')

    // The connection is up when the editor is.
    const editor = page.locator('.pane.active .nocx-editor-input')
    await expect(editor).toBeVisible({ timeout: 30_000 })
    // Open the Ports view: the replayed forward has no detected row to own
    // it (this container has no probe tool, so discovery says unavailable),
    // and renders as a forwarded row in Orphaned forwards — the same
    // three-action row the fix is about.
    await page.locator(VIEW_PORTS).click()
    // The RUNNING forwarded row, by state: a locator that can match two
    // rows is a spec that fails for reasons unrelated to what it asserts —
    // that is exactly the webkit strict-mode failure this spec was sent
    // back for (a stale failed row plus the live one for the same
    // destination). The product now supersedes the stale row too (a running
    // forward hides an earlier failure for the same destination, W7
    // revision), and the state scoping alone excludes it: the fixture's
    // single forward can yield at most one running row, because a second
    // replay would fail its bind rather than mint another running record.
    const row = page.locator('[data-testid="forwarded-row"][data-state="forwarded"]', {
      hasText: LONG_DESTINATION,
    })
    await expect(row).toBeVisible({ timeout: 30_000 })
    await expectDefaultRail(page)

    // The measurement, pointer NOT over the row: both lines fit. The
    // numbers are logged so a run's report carries evidence, not adjectives.
    await pointerAway(page)
    const dest = await measure(row.locator(DEST))
    console.log(`forwarded dest sw=${dest.sw} cw=${dest.cw}`)
    expect(dest.sw, `destination overflows (${dest.sw} > ${dest.cw})`).toBeLessThanOrEqual(dest.cw)
    const addr = await measure(row.locator(ADDR))
    console.log(`forwarded addr sw=${addr.sw} cw=${addr.cw}`)
    expect(addr.sw, `address overflows (${addr.sw} > ${addr.cw})`).toBeLessThanOrEqual(addr.cw)
    // The measurements above were taken with the actions hidden — the
    // pointer is away from the row, which is the state the bug report is
    // about.
    await expect(row.locator(ACTIONS)).toHaveCSS('opacity', '0')

    // Pointer reachability: hovering the row reveals the action group.
    await row.hover()
    await expect(row.locator(ACTIONS)).toHaveCSS('opacity', '1')

    // Keyboard reachability: focusing the Stop action (no hover) reveals it.
    await pointerAway(page)
    await row.locator('[data-testid="ports-stop"]').focus()
    await expect(row.locator(ACTIONS)).toHaveCSS('opacity', '1')
  } finally {
    // Take the profile back out: the stand's home is shared by every spec in
    // the run, and a stale profile would become the next spec's starting
    // state (nocx-8rda).
    try {
      const info = await wsInfo(page)
      if (createdId) await rpc(page, info.port, info.token, 'profiles.delete', { id: createdId })
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})
