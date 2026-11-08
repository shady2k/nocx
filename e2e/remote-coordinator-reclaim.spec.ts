/**
 * THE REMOTE HELPER EPIC'S ACCEPTANCE TEST (nocx-k6p18.9).
 *
 * What a user can do that they could not before: start a long build on a
 * remote host where the helper is installed, quit nocx ENTIRELY, come back,
 * and find the same pane still running — its command block there, the output
 * the host's own window dropped NAMED rather than silently absent, and the
 * build's real exit status when it finishes rather than "unknown".
 *
 * The three conditions that make this proof meaningful are all explicit:
 *
 *  1. The client goes away and so does the SERVER. The browser context is
 *     closed and then `nocx-server` is stopped, so what comes back is a fresh
 *     COORDINATOR, not a reconnect. A reload would keep the renderer's session
 *     map; a reconnect would keep the helper link.
 *  2. The command's output must overrun the ACTUAL helper window reported by
 *     `sessions.inventory` — not the coordinator's 256 KiB replay ring, which
 *     is a different buffer on a different machine — and it must do so WHILE
 *     NO COORDINATOR EXISTS. That last clause is the whole of the gap path:
 *     bytes the old coordinator already recorded are not lost, so a filler
 *     released before the server is stopped exercises nothing.
 *  3. Liveness is a marker COUNT taken from the remote process's own file. A
 *     pane that rendered after the return cannot make a remote process advance
 *     a file on the host.
 *
 * WHY THE HOLE IS `hostWindow` AND WHY THAT IS ASSERTED BY NAME (nocx-k6p18.25,
 * internal/content/ledger.go). Three holes reach a client and they are three
 * different facts: `cap` — the bytes were recorded HERE and this machine's
 * retention bound evicted them; `unrecorded` — nobody was recording; and
 * `hostWindow` — the EXECUTION HOST's own bounded window reclaimed them before
 * this machine could ever receive them. Only the third is what this scenario
 * produces, and each names a different knob to a person who wants the bytes
 * back. A gap of the wrong reason passing here would be the test agreeing with
 * a bug, so the reason is asserted and not merely the count.
 *
 * WHERE THE EXIT STATUS IS ASSERTED, AND WHY NOT ON THE BLOCK. The build ends
 * by exiting the shell, and a clean exit CLOSES ITS OWN TAB (terminal-content
 * .ts: "A clean exit closes the tab exactly as it always did"). There is
 * therefore no block left in the pane to carry a chip, and a spec that waited
 * for one would be waiting for something the product deliberately takes away.
 * The surface that outlives the tab is the notification centre, whose row is
 * written from `session.ExitOutcome` — "ended" with `exit status 7` for an
 * authoritative exit, "was interrupted" with nothing for a loss, which is
 * exactly the "unknown" this epic set out to stop (nocx-k6p18.23). That is the
 * discrimination the criterion asks for, so that is where it is asserted.
 *
 * The terminal grid is a canvas, so marker counts come from the remote
 * process's own file and the byte accounting from `sessions.inventory` and
 * `session.recoveryStatus` — the coordinator's metadata method, and the very one the
 * reclaim itself reads. The COMMAND BLOCK and the missing-output notice are
 * asserted in the product DOM, because "a block in the pane, not a line in a
 * log" is itself one of this bead's criteria (moved here from nocx-k6p18.22).
 *
 * NOTHING HERE WAITS OUT A DURATION. Every stage of the remote command parks
 * on a file the test creates, and every wait is a poll on an observable state:
 * a marker count, a byte count, a DOM node. The detached window is exactly as
 * long as it needs to be.
 *
 * ── GREEN, AND WHAT IT HAD TO BE RED ABOUT FIRST ──────────────────────────
 *
 * It passes end to end as of 2026-09-02 — four consecutive runs, the last of
 * them on the tree left behind after every mutation below had been applied and
 * reverted. It was red for a real defect every time the product had one, and
 * each of those was found by RUNNING this file rather than by reading code:
 * nocx-k6p18.24 (a helper-hosted tab rendered output and accepted no input),
 * nocx-k6p18.30 (no coordinator path re-adopted a live helper-hosted session,
 * so `sessions.live` answered `[]` for two minutes while the build was
 * provably still advancing), and nocx-k6p18.31 (a re-adopted session had no
 * lifecycle domain, so the pane read its shell as markerless, hid every
 * restored block behind `inner-fullscreen-mode` and offered no editor).
 *
 * A GREEN CHECK NOBODY HAS FALSIFIED IS A CHECK NOBODY HAS MEASURED. Four
 * mutations, each applied to the product, run, and reverted on 2026-09-02, and
 * the last three were run against this file EXACTLY as it stands:
 *
 *  - internal/helper/session/spawn_local.go, the lifecycle socketpair not
 *    built: red in 18.6 s at `promptReady` (harness.ts:39) — not at the block
 *    assertion. With no lifecycle channel the pane never shows a command
 *    editor, so the run dies one step before it can ask about a block. The
 *    mutation IS caught; the assertion that names it is the harness's.
 *  - internal/session/session.go, the `interface{ ExitCode() int }` branch
 *    deleted from `ExitOutcome`: red at `expect(returnedPane).toHaveCount(0)`.
 *    A status that classifies as a LOSS never closes the tab, so this is
 *    caught one assertion before the bell row it was aimed at.
 *  - internal/transport/ws_session_record.go, the execution host's window hole
 *    recorded as `unrecorded`: red at the recovery card's reason clause, which
 *    then reads "that was never recorded" and sends a person to a retention
 *    knob that governs nothing about the host's window.
 *  - internal/app/session_readopt.go, `LifecycleOffset` resumed at the
 *    helper's window BASE rather than at its head: red at the restored-block
 *    assertion, twice. Replaying an already-authenticated stretch of the
 *    lifecycle stream into the new kernel does not merely re-deliver frames
 *    that already ran — the pane never reaches `prompt_ready` at all, so it
 *    collapses into exactly the markerless layout nocx-k6p18.31 used to
 *    produce, editor and all.
 */
import { expect, type Browser, type Page } from '@playwright/test'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'

import { recoveryAccount, uncoveredGapRanges } from '../frontend/src/recovery-account'
import { formatBytes } from '../frontend/src/ui/format-bytes'
import { BASE_URL } from './base-url'
import {
  standalone as base,
  bindEndpoint,
  clickIntoEditor,
  openControlPlane,
  promptReady,
  showSidebarView,
  snapshotWatchedClients,
  watchClient,
  type BackendEndpoint,
  type DisposableRoot,
  VaultBackend,
} from './harness'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const test = base

const TAB = '.nocx-tab'
const VIEW_GIT = 'button[data-view="git"]'
const GIT_PANEL = '[data-testid="git-panel"]'

/** The bell's rail button and the panel's rows, in the panel's own vocabulary
 *  — the same two notification-centre.spec.ts uses, and for the same reason:
 *  the kit takes no test hooks, and inventing one here would be a second name
 *  for one button. */
const BELL = 'button[data-view="notifications"]'
const ROW = '.notifications-panel__list .ui-collection-row'

const MARKER_PREFIX = 'NOCXREMOTE'

/** How far the command's own per-second counter must advance while nothing is
 *  attached AND no coordinator is running. Not a duration this test waits out:
 *  it waits on the marker file, which is the command's own clock. Five is the
 *  floor below which "the process kept running" would be indistinguishable
 *  from "it was resumed and flushed". */
const MARKERS_WHILE_DETACHED = 5

/** The build's exit status. NON-ZERO deliberately: a zero exit reaches
 *  `ExitOutcome` as a nil wait error and would be classified correctly even
 *  with the helper's status mapping gone, so it could not tell a real status
 *  from a fabricated one. */
const EXIT_CODE = 7

interface LiveSession {
  sessionId: string
  instanceId: string
  sessionEpoch: number
  paneId: string | null
  replayFrom: number
  attached: boolean
}

interface InventorySession {
  hostSessionId: { generation: string; session: string }
  launch: { pid: number; pgid: number; windowBytes: number; cwd: string }
  window: { base: number; written: number }
  writer: string | null
  exit: { code: number; signal?: number; at: string } | null
}

interface RecoveryStatus {
  sessionId: string
  produced: number
  gaps: { start: number; end: number; reason: string }[]
}

interface ControlPlane {
  call: (method: string, params: unknown) => Promise<unknown>
  close: () => void
}

function paneForSession(page: Page, sessionId: string) {
  return page.locator(`.pane[data-session-id="${sessionId}"]`)
}

interface VisibleLiveOutputGap {
  title: string
  description: string
  count: number
  rect: { x: number; y: number; width: number; height: number }
}

async function captureFirstVisibleLiveOutputGap(page: Page, sessionId: string): Promise<void> {
  await page.addInitScript((targetSessionId) => {
    const captured = window as Window & { __nocxLiveOutputGapReading?: VisibleLiveOutputGap }
    let scheduled = false
    const observer = new MutationObserver(schedule)
    function schedule() {
      if (scheduled || captured.__nocxLiveOutputGapReading) return
      scheduled = true
      requestAnimationFrame(() => {
        scheduled = false
        const notices = document.querySelectorAll<HTMLElement>(
          `.pane[data-session-id="${targetSessionId}"] [data-notice-kind="live-output-gap"]`,
        )
        for (const notice of notices) {
          const rect = notice.getBoundingClientRect()
          if (
            rect.width <= 0 ||
            rect.height <= 0 ||
            rect.bottom <= 0 ||
            rect.right <= 0 ||
            rect.top >= innerHeight ||
            rect.left >= innerWidth
          ) {
            continue
          }
          let visible = true
          for (let node: HTMLElement | null = notice; node; node = node.parentElement) {
            const style = getComputedStyle(node)
            if (
              style.display === 'none' ||
              style.visibility === 'hidden' ||
              style.visibility === 'collapse' ||
              Number(style.opacity) === 0
            ) {
              visible = false
              break
            }
          }
          if (!visible) continue
          captured.__nocxLiveOutputGapReading = {
            title: notice.querySelector('.ui-status-card__title')?.textContent ?? '',
            description: notice.querySelector('.ui-status-card__desc')?.textContent ?? '',
            count: notices.length,
            rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
          }
          observer.disconnect()
          return
        }
      })
    }
    observer.observe(document, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
    })
    schedule()
  }, sessionId)
}

async function freshClient(
  browser: Browser,
  endpoint: BackendEndpoint,
  label: string,
  onServerFrame?: (frame: string) => void,
  beforeNavigate?: (page: Page) => Promise<void>,
): Promise<Page> {
  // baseURL explicitly: a context made by hand inherits nothing from the
  // config's `use`, so `goto('/')` would have no origin to resolve against.
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  if (onServerFrame) {
    page.on('websocket', (socket) => {
      if (new URL(socket.url()).port !== String(endpoint.port)) return
      socket.on('framereceived', (data) => {
        const frame =
          typeof data.payload === 'string' ? data.payload : data.payload.toString('utf8')
        onServerFrame(frame)
      })
    })
  }
  // On the failure report from its first byte, and the harness's to close.
  watchClient(page, label)
  await bindEndpoint(page, endpoint)
  await beforeNavigate?.(page)
  await page.goto('/')
  return page
}

async function withTimeout<T>(promise: Promise<T>, label: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error(`e2e: ${label} timed out`)), 10_000)
      }),
    ])
  } finally {
    clearTimeout(timer)
  }
}

/**
 * A control-plane socket of the TEST'S own, opened for one question and shut
 * again. It never attaches to a session, so asking a question cannot itself
 * make the session attached — which would silently repair condition 1.
 */
async function ask<T>(endpoint: BackendEndpoint, method: string, params: unknown): Promise<T> {
  let wire: ControlPlane | null = null
  const opening = openControlPlane(endpoint.port, endpoint.token)
  try {
    wire = await withTimeout(opening, `${method} handshake`)
    return (await withTimeout(wire.call(method, params), `${method} call`)) as T
  } catch (error) {
    void opening.then(
      (lateWire) => lateWire.close(),
      () => undefined,
    )
    throw error
  } finally {
    wire?.close()
  }
}

async function liveSessions(endpoint: BackendEndpoint): Promise<LiveSession[]> {
  return (await ask<{ sessions: LiveSession[] }>(endpoint, 'sessions.live', {})).sessions
}

async function inventory(endpoint: BackendEndpoint): Promise<InventorySession[]> {
  return (await ask<{ sessions: InventorySession[] }>(endpoint, 'sessions.inventory', {})).sessions
}

function helperSession(
  entries: InventorySession[],
  sessionId: string,
): InventorySession | undefined {
  return entries.find((entry) => entry.hostSessionId.session === sessionId)
}

async function recoveryStatus(
  endpoint: BackendEndpoint,
  live: Pick<LiveSession, 'sessionId' | 'instanceId' | 'sessionEpoch'>,
): Promise<RecoveryStatus> {
  return ask<RecoveryStatus>(endpoint, 'session.recoveryStatus', {
    sessionId: live.sessionId,
    instanceId: live.instanceId,
    sessionEpoch: live.sessionEpoch,
  })
}

function markersIn(text: string): number[] {
  return [...text.matchAll(/NOCXREMOTE-(\d{3})/g)].map((match) => Number(match[1]))
}

/** The fixture's remote HOME is a real filesystem, so reading this file is a
 *  far-side observation without opening another ssh shell that could itself
 *  become blocked behind the running pty. ENOENT is the only expected
 *  absence. */
function markerCountOnFixtureHost(markerFile: string): number {
  let raw: string
  try {
    raw = readFileSync(markerFile, 'utf8')
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return 0
    throw error
  }
  return raw.split('\n').filter((line) => /^NOCXREMOTE-\d{3}$/.test(line)).length
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`
}

interface RemoteGates {
  /** One line per iteration, appended on the host. The command's own clock. */
  markerFile: string
  /** Written by the TEST once the coordinator is gone. Until it exists the
   *  build has printed exactly one marker and no filler at all. */
  detached: string
  /** Written by the COMMAND once the whole filler has left its stdout, so the
   *  test brings the coordinator back on an observed state rather than on a
   *  guess about how fast a pty drains. */
  fillerDone: string
  /** Written by the TEST when every reclaim assertion has been made. The build
   *  exits only then, so it is still running for all of them and nothing here
   *  depends on it lasting long enough. */
  finish: string
}

/**
 * The build: one marker a second until told to stop, with a filler burst
 * released by the test and an exit released by the test.
 *
 * Explicitly exec POSIX sh: the host's real login shell can be fish, whose
 * syntax does not accept this fixture's assignments, loops or arithmetic.
 *
 * The filler is NUL bytes, which count toward the helper window without
 * flooding the terminal grid. The live-gap case inserts its visible marker
 * after a 256 KiB NUL prefix so the browser can prove later output before it
 * consumes the remaining filler.
 *
 * WHY `exec` AND NOT A SUBSHELL. The status this bead cares about is the
 * SESSION's — the one the helper reports and `ExitOutcome` maps — so replace
 * the login shell rather than returning to it when the script exits. Its
 * exit status remains the session's exit status, not merely a command result.
 */
function longRemoteCommand(
  gates: RemoteGates,
  fillerBytes: number,
  markerAfterPrefixBytes?: number,
): string {
  const marker = shellQuote(gates.markerFile)
  const detached = shellQuote(gates.detached)
  const fillerDone = shellQuote(gates.fillerDone)
  const finish = shellQuote(gates.finish)
  const markerMidFiller =
    markerAfterPrefixBytes === undefined
      ? ''
      : [
          `head -c ${markerAfterPrefixBytes} /dev/zero`,
          `printf '${MARKER_PREFIX}-%03d\\n' 1 >> ${marker}`,
          `printf '${MARKER_PREFIX}-%03d\\n' 1`,
          `head -c ${fillerBytes - markerAfterPrefixBytes} /dev/zero`,
          'i=1',
        ].join('; ')
  const filler =
    markerAfterPrefixBytes === undefined ? `head -c ${fillerBytes} /dev/zero` : markerMidFiller
  const script = [
    'i=0',
    `while [ ! -e ${finish} ]; do printf '${MARKER_PREFIX}-%03d\\n' "$i" >> ${marker}`,
    `printf '${MARKER_PREFIX}-%03d\\n' "$i"`,
    `if [ "$i" -eq 0 ]; then while [ ! -e ${detached} ]; do sleep 1; done`,
    `${filler}; printf '\\r'; : > ${fillerDone}; fi`,
    'i=$((i+1)); sleep 1; done',
    `exit ${EXIT_CODE}`,
  ].join('; ')
  return `exec sh -c ${shellQuote(script)}`
}

function seedKnownHost(backend: VaultBackend, fixture: SshdFixture): void {
  const sshDir = join(backend.isolatedHome, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
}

async function openSavedProfile(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible({ timeout: 10_000 })
  await search.fill(profileName)
  const option = page.locator('.quick-connect__item', { hasText: profileName })
  await expect(option).toBeVisible({ timeout: 10_000 })
  await page.keyboard.press('Enter')
}

async function createProfileAndOpen(
  page: Page,
  endpoint: BackendEndpoint,
  fixture: SshdFixture,
): Promise<string> {
  const profileName = `e2e-remote-reclaim-${Date.now()}`
  await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
    type: 'ssh',
    name: profileName,
    options: {
      host: fixture.host,
      port: fixture.port,
      user: 'e2e',
      keyPath: fixture.userKey,
    },
  })
  await openSavedProfile(page, profileName)
  return profileName
}

/** The shipped install gesture: opening this AUTO connection for the first
 *  time raises the connect-time ask (ADR-0069) — the host key is already
 *  trusted (seedKnownHost, above), so the ask is method-only, on the same
 *  one-dialog surface the host-key ask uses — and choosing Helper puts the
 *  helper on the host. The open itself does not resolve, and the tab does
 *  not appear, until the ask is answered.
 *
 *  The shell that comes up is NOT inside the seeded repository. Since
 *  ADR-0057 every pane is helper-hosted: the far nocx-helper spawns it
 *  through internal/pty (NewLocal), whose resolveCwd falls back to the
 *  login user's home when no cwd is given — the same place a REAL sshd
 *  starts a login shell. e2e-sshd's own `-repo` seeding only chdirs a shell
 *  IT serves directly, so it never reaches this one. This function moves
 *  into the repository the way a person would, with a typed `cd`, and waits
 *  on the prompt editor's own cwd chip before asking the panel to open — the
 *  same observable signal git-remote.spec.ts and local-drop.spec.ts wait on.
 *  The branch line appearing afterwards is then the panel's own statement
 *  that the helper answered for that repository; the git panel itself never
 *  asks (ADR-0068). */
async function installHelperThroughProduct(
  page: Page,
  endpoint: BackendEndpoint,
  fixture: SshdFixture,
  repoPath: string,
): Promise<string> {
  const profileName = await createProfileAndOpen(page, endpoint, fixture)
  const helperDialog = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
  await expect(helperDialog).toBeVisible({ timeout: 30_000 })
  await helperDialog.getByRole('radio', { name: 'Helper' }).click()
  await helperDialog.getByRole('button', { name: 'Continue' }).click()
  await expect(helperDialog).not.toBeVisible()
  await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })
  await promptReady(page)
  // The editor can be focused before shell integration finishes; wait for the
  // product's observable readiness state before sending the cd command.
  await expect(
    page.locator('.pane.active').getByRole('status', { name: 'Checking this shell…' }),
  ).toHaveCount(0, {
    timeout: 30_000,
  })
  await page.keyboard.type(`cd '${repoPath}'`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .nocx-editor-context .ui-prompt-context')).toContainText(
    basename(repoPath),
    {
      timeout: 30_000,
    },
  )
  await page.locator(VIEW_GIT).click()
  await expect(page.locator(GIT_PANEL)).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-testid="git-branch"]')).toBeVisible({ timeout: 60_000 })
  return profileName
}

/** Wait for a file the REMOTE command creates. The fixture's host filesystem
 *  is this one, so this is a far-side observation and not a duration. */
async function waitForRemoteFile(path: string, what: string): Promise<void> {
  await expect
    .poll(() => existsSync(path), { timeout: 120_000, intervals: [250], message: what })
    .toBe(true)
}

test('a remote helper build survives a fresh coordinator, names what it lost, and reports its real exit status', async ({
  browser,
}) => {
  test.setTimeout(900_000)

  const backendRoot: DisposableRoot = {
    root: mkdtempSync(join(tmpdir(), 'nocx-remote-reclaim-backend-')),
  }
  const remoteRoot = mkdtempSync(join(tmpdir(), 'nocx-remote-reclaim-host-'))
  const remoteHome = join(remoteRoot, 'home')
  const remoteCwd = join(remoteRoot, 'repo')
  const gates: RemoteGates = {
    markerFile: join(remoteHome, 'markers.txt'),
    detached: join(remoteHome, 'detached'),
    fillerDone: join(remoteHome, 'filler-done'),
    finish: join(remoteHome, 'finish'),
  }
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  mkdirSync(remoteCwd, { recursive: true, mode: 0o700 })

  const backend = new VaultBackend(readStand().server, backendRoot)
  let fixture: SshdFixture | null = null
  let first: Page | null = null
  let returned: Page | null = null

  try {
    fixture = await startSshd({ home: remoteHome, cwd: remoteCwd, args: ['-repo', remoteCwd] })
    let endpoint = await backend.start()
    seedKnownHost(backend, fixture)

    // ── a host where the helper is installed ──────────────────────────────
    //
    // Through the product's own consent-and-SFTP path, so what the rest of
    // this test opens is a host a person could have set up.
    first = await freshClient(browser, endpoint, 'first client')
    await promptReady(first)
    const profileName = await installHelperThroughProduct(first, endpoint, fixture, remoteCwd)

    // ── the build's tab, and it really is helper-hosted ───────────────────
    //
    // A second connection to the same profile, now that the helper is there.
    // The inventory entry below is what proves this is not the conventional
    // ssh path wearing the same tab: a conventional session has no
    // hostSessionId and appears in no inventory at all.
    await openSavedProfile(first, profileName)
    await expect(first.locator(TAB)).toHaveCount(3, { timeout: 30_000 })
    const activePane = first.locator('.pane.active')
    await expect(activePane).toHaveAttribute('data-session-id', /.+/, { timeout: 90_000 })
    const commandSessionId = await activePane.getAttribute('data-session-id')
    expect(commandSessionId).not.toBeNull()
    const commandPane = paneForSession(first, commandSessionId!)
    await expect(commandPane).toBeVisible()

    let beforeCommand: { live: LiveSession; host: InventorySession } | null = null
    await expect
      .poll(
        async () => {
          const sessions = await liveSessions(endpoint)
          const entries = await inventory(endpoint)
          const live = sessions.find(
            (session) =>
              session.sessionId === commandSessionId &&
              session.paneId !== null &&
              helperSession(entries, session.sessionId) !== undefined,
          )
          const host = live ? helperSession(entries, live.sessionId) : undefined
          beforeCommand = live && host ? { live, host } : null
          return beforeCommand !== null
        },
        { timeout: 90_000, message: 'the tab that opened is not a helper-hosted session' },
      )
      .toBe(true)
    const liveBefore = beforeCommand!.live
    const hostBefore = beforeCommand!.host
    expect(hostBefore.hostSessionId.session).toBe(liveBefore.sessionId)
    expect(hostBefore.hostSessionId.generation).not.toBe('')
    expect(hostBefore.launch.pid).toBeGreaterThan(0)
    expect(hostBefore.launch.pgid).toBeGreaterThan(0)
    expect(hostBefore.launch.windowBytes).toBeGreaterThan(0)
    expect(hostBefore.writer).not.toBeNull()

    const paneDomId = await commandPane.getAttribute('id')
    expect(paneDomId).toMatch(/^pane-/)
    await first.locator(`.nocx-tab[aria-controls="${paneDomId}"]`).click()
    await promptReady(first)

    // `promptReady` checks editor focus, not shell-integration readiness.
    // While the accessible status is present, the product hides scrollback,
    // so a running command block exists but cannot be seen. Settle that state
    // before starting the fixture command.
    await expect(commandPane.getByRole('status', { name: 'Checking this shell…' })).toHaveCount(0, {
      timeout: 30_000,
    })

    // ── the build starts ──────────────────────────────────────────────────
    //
    // The filler is sized from the HOST's own window, read out of its
    // inventory a moment ago — never from the coordinator's replay ring,
    // which is a different bound on a different machine.
    const markerCommand = longRemoteCommand(
      gates,
      hostBefore.launch.windowBytes + 64 * 1024,
      256 * 1024,
    )
    await clickIntoEditor(first)
    await first.keyboard.type(markerCommand)
    await first.keyboard.press('Enter')

    // A running block can hold the ECHOED command text before the remote
    // shell has accepted it, so the marker file is the first authoritative
    // proof that the helper's pty actually executed this.
    await expect
      .poll(() => markerCountOnFixtureHost(gates.markerFile), {
        timeout: 60_000,
        intervals: [250],
        message: 'the remote command did not write its first marker',
      })
      .toBeGreaterThan(0)

    // AND THE PRODUCT SHOWS IT AS A BLOCK. One of this bead's criteria, moved
    // here from nocx-k6p18.22: a command on a helper-hosted session produces a
    // command block in the pane, not a line in a log.
    //
    // MEASURED FALSIFICATION, and it does not land where you would expect.
    // Removing the lifecycle socketpair from internal/helper/session/
    // spawn_local.go turns this run red in seconds (22 s, and 18.6 s when it
    // was re-measured on 2026-09-02) — but at `promptReady` above,
    // not here: with no lifecycle channel the pane never shows the command
    // editor at all, so the run dies on a hidden `.nocx-editor-input` one step
    // before it can ask about a block. The mutation IS caught; the assertion
    // that names it is the harness's, not this one. Left recorded rather than
    // engineered around, because moving the block assertion earlier would only
    // rename the same failure.
    await expect(
      commandPane.locator('.cmd-block.cmd-block-running').filter({ hasText: MARKER_PREFIX }),
    ).toBeVisible({ timeout: 60_000 })

    const markerAtDetach = markerCountOnFixtureHost(gates.markerFile)
    const writtenAtDetach = helperSession(await inventory(endpoint), liveBefore.sessionId)!.window
      .written
    const producedAtDetach = (await recoveryStatus(endpoint, liveBefore)).produced

    // ── the window closes, and then so does nocx ──────────────────────────
    //
    // The context first, and the server is asked to confirm it saw the
    // attachment go rather than the test inferring it from an API returning.
    await first.context().close()
    first = null
    await expect
      .poll(
        async () =>
          (await liveSessions(endpoint)).find((s) => s.sessionId === liveBefore.sessionId)
            ?.attached,
        {
          timeout: 30_000,
          message: 'the client context closed but the coordinator still reports it attached',
        },
      )
      .toBe(false)

    // Now the coordinator itself. Everything the build prints from here until
    // the fresh one connects is produced with NOBODY recording — which is the
    // only state in which the host's window can take bytes this machine never
    // receives, and therefore the only state in which the gap under test
    // exists at all.
    const oldToken = endpoint.token
    backend.stop()
    expect(backend.running).toBe(false)

    // ── the build overruns the host's window with nocx gone ───────────────
    writeFileSync(gates.detached, 'detached\n')
    await waitForRemoteFile(
      gates.fillerDone,
      'the remote build never finished writing past the helper window',
    )
    await expect
      .poll(() => markerCountOnFixtureHost(gates.markerFile), {
        timeout: 120_000,
        intervals: [1_000],
        message: 'the remote marker count did not advance while no coordinator existed',
      })
      .toBeGreaterThanOrEqual(markerAtDetach + MARKERS_WHILE_DETACHED)
    const markerBeforeReturn = markerCountOnFixtureHost(gates.markerFile)

    // ── nocx comes back, and it is a stranger ─────────────────────────────
    endpoint = await backend.start()
    expect(endpoint.token).not.toBe(oldToken)
    expect(backend.running).toBe(true)

    // THE COORDINATOR REDISCOVERS THE BUILD BEFORE ANY CLIENT ASKS FOR IT,
    // and this is asserted here rather than left to the pane because it is
    // the step everything after it stands on. A restored pane reclaims by
    // CLAIMING an entry out of `sessions.live` keyed by its pane id
    // (panes.ts, primeLiveSessions/adoptionFor); with no entry the restore
    // has no claim to make and falls back to opening a second shell to the
    // same host — which looks like a connection failure in the tab and is
    // really a session that was never taken back. Asserted on the server, so
    // the diagnosis does not depend on reading a renderer's mind.
    await expect
      .poll(async () => (await liveSessions(endpoint)).map((s) => s.sessionId), {
        timeout: 120_000,
        intervals: [1_000],
        message:
          'the fresh coordinator does not list the still-running helper session, so no restored pane can claim it',
      })
      .toContain(liveBefore.sessionId)

    // A genuinely new browser context on a genuinely new coordinator: an
    // empty renderer session map, and a token neither of them has seen. Capture
    // control frames before navigation so the browser's actual session.outputGap
    // notification cannot race the assertions below.
    let liveOutputGap: { sessionId: string; start: number; end: number; reason: string } | null =
      null
    // EVERY range the returned client is told about, in arrival order: the
    // reclaim card subtracts them from the recording's account so the same
    // lost bytes are never named twice, and the assertion below has to derive
    // the card's number the same way (nocx-zg3k3.14.24).
    const liveOutputGaps: { sessionId: string; start: number; end: number; reason: string }[] = []
    returned = await freshClient(
      browser,
      endpoint,
      'returned client',
      (frame) => {
        if (typeof frame !== 'string') return
        let envelope: { method?: unknown; params?: Record<string, unknown> }
        try {
          envelope = JSON.parse(frame) as { method?: unknown; params?: Record<string, unknown> }
        } catch {
          return
        }
        const params = envelope.params
        if (
          envelope.method === 'session.outputGap' &&
          params &&
          params.sessionId === liveBefore.sessionId &&
          typeof params.start === 'number' &&
          typeof params.end === 'number' &&
          typeof params.reason === 'string'
        ) {
          liveOutputGap = {
            sessionId: params.sessionId as string,
            start: params.start,
            end: params.end,
            reason: params.reason,
          }
          liveOutputGaps.push(liveOutputGap)
        }
      },
      (page) => captureFirstVisibleLiveOutputGap(page, liveBefore.sessionId),
    )
    const returnedPane = paneForSession(returned, liveBefore.sessionId)
    await expect(returnedPane).toBeVisible({ timeout: 120_000 })

    let liveAfter: LiveSession | null = null
    await expect
      .poll(
        async () => {
          const sessions = await liveSessions(endpoint)
          liveAfter = sessions.find((session) => session.sessionId === liveBefore.sessionId) ?? null
          return liveAfter !== null
        },
        { timeout: 60_000, message: 'the fresh coordinator did not reclaim the helper session' },
      )
      .toBe(true)
    expect(liveAfter!.sessionId).toBe(liveBefore.sessionId)
    expect(liveAfter!.attached).toBe(true)

    // THE SAME PROCESS ON THE HOST, not a new one wearing the old id.
    let hostAfter: InventorySession | null = null
    await expect
      .poll(
        async () => {
          hostAfter = helperSession(await inventory(endpoint), liveBefore.sessionId) ?? null
          return hostAfter !== null
        },
        {
          timeout: 60_000,
          message: 'the fresh coordinator cannot inventory the original hostSessionId',
        },
      )
      .toBe(true)
    expect(hostAfter!.hostSessionId).toEqual(hostBefore.hostSessionId)
    expect(hostAfter!.launch.pid).toBe(hostBefore.launch.pid)
    expect(hostAfter!.launch.windowBytes).toBe(hostBefore.launch.windowBytes)
    expect(hostAfter!.exit).toBeNull()

    // ── condition 2, stated as a number ───────────────────────────────────
    //
    // How far the build got with no coordinator listening, measured against
    // the HOST's own bound. Below it the window would have held everything
    // and the gap path would never have been entered.
    const detachedBytes = hostAfter!.window.written - writtenAtDetach
    expect(detachedBytes).toBeGreaterThan(hostBefore.launch.windowBytes)

    // ── condition 3: the process RAN, it was not resumed ──────────────────
    expect(markerBeforeReturn - markerAtDetach).toBeGreaterThanOrEqual(MARKERS_WHILE_DETACHED)

    // ── the pane is the one that never stopped ────────────────────────────
    //
    // The block criterion again, and this is the half that matters: blocks
    // survive a coordinator replacement (nocx-k6p18.22, moved here).
    //
    // `toBeVisible` RATHER THAN `toBeAttached`, and that word is the whole
    // assertion. This line was red for nocx-k6p18.31 and it is the line the
    // fourth mutation lands on, and in BOTH cases the block is built and is in
    // the DOM — `<div class="cmd-block" data-restored="true"
    // data-output-evicted="true">`, resolving 120-odd times across 60 s — and
    // is `display: none`. A pane with no lifecycle domain reads its shell as
    // markerless, enters `unstructured` mode, and
    // `.scrollback-inner.inner-fullscreen-mode > *:not(.terminal-live-container)`
    // takes every block in the stack with it (style.css; controller.ts
    // `setUnstructured`). A history a person cannot see has not survived
    // anything, so `toBeAttached` here would be this test agreeing with the
    // defect — it passes in both of the states above.
    const returnedBlock = returnedPane
      .locator('.cmd-block')
      .filter({ hasText: MARKER_PREFIX })
      .first()
    await expect(returnedBlock).toBeVisible({ timeout: 60_000 })

    // ── the actual live event and its distinct notice ──────────────────────
    // The new client must receive the control-plane event, not merely recover
    // historical gap metadata. Its live title is deliberately distinct from
    // the reclaim card below.
    await expect
      .poll(() => liveOutputGap, {
        timeout: 60_000,
        message: 'the returned browser did not receive session.outputGap for the reclaimed session',
      })
      .not.toBeNull()
    expect(liveOutputGap!.sessionId).toBe(liveBefore.sessionId)
    expect(liveOutputGap!.reason).toBe('hostWindow')
    expect(liveOutputGap!.end).toBeGreaterThan(liveOutputGap!.start)

    // The live warning expires after eight seconds; restoration above may
    // legitimately take longer. Assert what was actually visible on its first
    // paint, not whether a transient card survived these unrelated waits.
    const liveReading = await returned.evaluate(
      () =>
        (window as Window & { __nocxLiveOutputGapReading?: VisibleLiveOutputGap })
          .__nocxLiveOutputGapReading ?? null,
    )
    expect(liveReading, 'no visible live-output warning was captured').not.toBeNull()
    expect(liveReading!.count).toBe(1)
    expect(liveReading!.rect.width).toBeGreaterThan(0)
    expect(liveReading!.rect.height).toBeGreaterThan(0)
    const covered: Array<{ start: number; end: number }> = []
    const prefixTitles: string[] = []
    let missing = 0
    for (const gap of liveOutputGaps) {
      const uncovered = uncoveredGapRanges(gap, covered)
      covered.push(...uncovered)
      missing += uncovered.reduce((total, range) => total + range.end - range.start, 0)
      if (missing > 0) prefixTitles.push(`${missing} bytes of live output are missing`)
    }
    expect(prefixTitles).toContain(liveReading!.title)
    expect(liveReading!.description).toBe("The execution host's output window moved past this tab.")

    // ── what remains missing, and the SAME number the ledger states ───────
    //
    // THE CARD'S NUMBER IS NOT A PHOTOGRAPH OF THE RECLAIM (nocx-zg3k3.14.24).
    // The reclaim read session.recoveryStatus before it attached — while the
    // recorder was still appending the bytes the helper replayed — and the
    // retention bound goes on evicting as it does. On PR #380's CI this
    // assertion compared that stale number with a fresh reading (3.1 MB
    // against 3.6 MB) and failed on a product that was only doing what it was
    // written to do. The fix is not a tolerance: the recording now PUSHES its
    // revised answer (session.recoveryChanged) and the card is rebuilt from
    // it, so `session.recoveryStatus` and the card are one fact at one instant.
    //
    // THE EXPECTED VALUE IS DERIVED BY THE PRODUCT, NOT BY THIS FILE.
    // `recoveryAccount` is what the card renders: every missing byte across
    // all reasons, minus the ranges the live notice already names. Summing the
    // recording's `cap` gaps here instead — which this spec used to do — is a
    // second owner of that arithmetic, and it states a different fact from the
    // card's the moment anything but the cap is missing.
    const notice = returnedPane
      .locator('.nocx-recovery-notice')
      .filter({ hasText: /of this session's output is missing/i })
    await expect(notice).toBeVisible({ timeout: 60_000 })

    // A WAIT ON THE CARD'S OWN VALUE, not on a duration and not a retry: the
    // poll settles the instant the two agree, and it agrees because the
    // recording told the pane what its answer became. Both numbers are read
    // inside the same pass, so what is asserted is that the card and the
    // ledger agree NOW — which is the whole of the criterion.
    await expect
      .poll(
        async () => {
          const ledger = await recoveryStatus(endpoint, liveAfter!)
          const account = recoveryAccount(ledger, liveOutputGaps)
          const stated =
            account === null
              ? null
              : `${formatBytes(account.missing)} of this session's output is missing`
          const card = await notice.locator('.ui-status-card__title').textContent()
          return card === stated ? null : `card ${String(card)} / ledger ${String(stated)}`
        },
        {
          timeout: 60_000,
          intervals: [250],
          message: 'the card never reached the number the recording states',
        },
      )
      .toBeNull()

    const finalOutput = await recoveryStatus(endpoint, liveAfter!)
    const capLossBytes = finalOutput.gaps
      .filter((gap) => gap.reason === 'cap')
      .reduce((total, gap) => total + gap.end - gap.start, 0)
    expect(capLossBytes).toBeGreaterThan(0)
    await expect(notice.locator('.ui-status-card__desc')).toContainText(
      "this session's own size limit dropped",
    )
    await expect(notice.locator('.ui-status-card__desc')).not.toContainText('hostWindow')

    // The same fact off the coordinator's own method, where the reason is a
    // field rather than a sentence.
    expect(finalOutput.gaps.length).toBeGreaterThan(0)
    const hostWindowGaps = finalOutput.gaps.filter((gap) => gap.reason === 'hostWindow')
    expect(hostWindowGaps.length).toBeGreaterThan(0)
    expect(
      hostWindowGaps.some(
        (gap) => gap.start === liveOutputGap!.start && gap.end === liveOutputGap!.end,
      ),
    ).toBe(true)
    expect(hostWindowGaps.some((gap) => gap.end - gap.start > 0)).toBe(true)
    expect(finalOutput.produced).toBeGreaterThan(producedAtDetach)
    // The earliest marker after the gap can leave this live viewport while
    // the remaining recovery checks run. Assert a visible post-start row in
    // the accessible grid instead of pinning the check to marker 001.
    const liveMarkers = returnedPane
      .getByRole('grid', { name: 'Terminal output' })
      .getByRole('row')
      .filter({ hasText: new RegExp(`${MARKER_PREFIX}-(?!000)\\d{3}`) })
    await expect(liveMarkers.first()).toBeVisible()

    // The markers the recording DOES hold are one process's output in stream
    // order — not two runs spliced together at the reclaim.
    const finalMarkers = markersIn(
      (await returnedPane.locator('.terminal-live-grid').textContent()) ?? '',
    )
    expect(finalMarkers.length).toBeGreaterThan(1)
    expect(Math.max(...finalMarkers)).toBeGreaterThan(Math.min(...finalMarkers))

    // ── still running THROUGH the reclaim, not merely up to it ────────────
    await expect
      .poll(() => markerCountOnFixtureHost(gates.markerFile), {
        timeout: 120_000,
        intervals: [1_000],
        message: 'the remote build stopped advancing once the fresh coordinator attached',
      })
      .toBeGreaterThan(markerBeforeReturn)

    // ── and when it finishes, the status is the real one ──────────────────
    //
    // Released only now, so nothing above depended on the build lasting long
    // enough. The tab closes itself on a clean exit, so the surface that
    // outlives it is the bell — where "ended" and the status are written from
    // ExitOutcome, and where a discarded helper status reads as "interrupted"
    // with nothing at all (nocx-k6p18.23).
    writeFileSync(gates.finish, 'finish\n')

    // THIS pane goes, which is what a clean exit does. Asserted on the pane
    // rather than on a tab count: the other tabs in this window have their own
    // reasons to come and go across a coordinator replacement, and a count
    // would be measuring them too.
    await expect(returnedPane).toHaveCount(0, { timeout: 120_000 })

    // The bell outlives it, and the row carries the STATUS. A helper status
    // that was discarded reaches this surface as "was interrupted" with no
    // body at all, so the number below is the whole discrimination.
    await showSidebarView(returned, 'notifications')
    await expect(returned.locator(BELL)).toBeVisible()
    const endedRow = returned.locator(ROW).filter({ hasText: `exit status ${EXIT_CODE}` })
    await expect(endedRow).toHaveCount(1, { timeout: 60_000 })
    await expect(endedRow).toContainText(/ended/)

    // And the host agrees — by having let the session go. Closer 2
    // (nocx-isjh4) changed what "the host agrees" means here: once the
    // coordinator has RECORDED a shell's exit and closed its block — which
    // the bell row just proved above — it calls EndSession, and the helper
    // keeps an exited session's row only for as long as no coordinator has
    // taken the result. So the host's own agreement is no longer a
    // lingering `exit.code` this test can poll for after the fact; it is
    // the row's ABSENCE, the same release `sessions.live` and
    // `sessions.inventory` show for every session a coordinator has
    // finished with. Polled on that state, not on a duration, because the
    // release races the bell row above by an amount this test does not
    // control.
    await expect
      .poll(
        async () => helperSession(await inventory(endpoint), liveBefore.sessionId) !== undefined,
        { timeout: 60_000, message: 'the host still lists a session it has already ended' },
      )
      .toBe(false)

    console.log(
      `remote reclaim: windowBytes=${hostBefore.launch.windowBytes} detachedBytes=${detachedBytes} ` +
        `markersAtDetach=${markerAtDetach} markersBeforeReturn=${markerBeforeReturn} ` +
        `gaps=${JSON.stringify(finalOutput.gaps)}`,
    )
  } finally {
    // Before the backend stops and paints its reconnect overlay over what the
    // failure left on screen. The clients themselves are the harness's to
    // close (watchClient), AFTER it has printed the failure context.
    await snapshotWatchedClients()
    backend.stop()
    fixture?.proc.kill('SIGKILL')
    rmSync(remoteRoot, { recursive: true, force: true })
    rmSync(backendRoot.root, { recursive: true, force: true })
  }
})
