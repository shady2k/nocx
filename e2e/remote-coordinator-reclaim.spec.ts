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
 * `session.output` — the coordinator's own methods, and the very ones the
 * reclaim itself reads. The COMMAND BLOCK and the missing-output notice are
 * asserted in the product DOM, because "a block in the pane, not a line in a
 * log" is itself one of this bead's criteria (moved here from nocx-k6p18.22).
 *
 * NOTHING HERE WAITS OUT A DURATION. Every stage of the remote command parks
 * on a file the test creates, and every wait is a poll on an observable state:
 * a marker count, a byte count, a DOM node. The detached window is exactly as
 * long as it needs to be.
 *
 * ── THIS SPEC IS RED, AND WHAT IT IS RED ABOUT IS THE POINT ────────────────
 *
 * THE RECLAIM ITSELF HOLDS (nocx-k6p18.30, nocx-xhm9e, measured 2026-09-02).
 * The helper installs through the product's consent path; the session is
 * helper-hosted with a hostSessionId; a block appears while it runs; the
 * detach is observed server-side; the build overruns the host's 4 MiB window
 * with no coordinator alive; a FRESH coordinator lists the session, a fresh
 * context finds its pane, the host reports the SAME pid and window and no
 * exit, the detached span is proved by the build's own marker file, the hole
 * is named `hostWindow`, the build goes on advancing THROUGH the reclaim, and
 * when it ends the notification centre carries `exit status 7`. Every one of
 * those was watched passing.
 *
 * ONE ASSERTION IS RED, and it is the last thing between this file and the
 * epic's sentence: the restored command block is in the DOM and is invisible.
 * A re-adopted session gets no lifecycle channel — the capability was minted
 * by the dead coordinator's kernel — so the pane sees a markerless shell,
 * enters `unstructured` mode, and every block in `.scrollback-inner` is
 * `display: none` (style.css `inner-fullscreen-mode`, controller.ts
 * `setUnstructured`). The same cause takes the command editor away, so the
 * returned tab cannot start a new command either, and the Git panel says the
 * session has no shell integration. That is nocx-k6p18.31. It is not fixable
 * from this file and it may not be assumed away here: a tab that comes back
 * with its history invisible and its editor gone has not come back.
 *
 * WHAT THE MUTATIONS SAY — each applied to the product, run, and reverted on
 * 2026-09-02, because a check nobody has falsified is a check nobody has
 * measured. Removing the lifecycle socketpair from
 * internal/helper/session/spawn_local.go: red in 18.6 s at `promptReady`
 * (harness.ts:39, from the `promptReady` on the build's own tab below). Deleting the
 * `interface{ ExitCode() int }` branch from internal/session/session.go's
 * `ExitOutcome`: red at `expect(returnedPane).toHaveCount(0)` — a status that
 * classifies as a LOSS never closes the tab, so the mutation is caught one
 * assertion EARLIER than the bell row it was aimed at. Recording the
 * host-window hole as `unrecorded` in internal/transport/ws_session_record.go:
 * red at the recovery card's reason clause. The last two were reached by
 * neutralising the nocx-k6p18.31 assertion locally; with it in place they sit
 * behind it, and that neutralised run is also where "every one of those was
 * watched passing" above comes from.
 */
import { test as base, expect, type Browser, type Page } from '@playwright/test'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { BASE_URL } from './base-url'
import {
  bindEndpoint,
  clickIntoEditor,
  openControlPlane,
  promptReady,
  showSidebarView,
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
const GIT_CONSENT = '[data-testid="git-consent-required"]'
const GIT_ACCEPT = '[data-testid="git-consent-accept"]'

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

interface SessionOutput {
  sessionId: string
  from: number
  effectiveSize: { cols: number; rows: number }
  produced: number
  runs: { offset: number; body: string }[]
  gaps: { start: number; end: number; reason: string }[]
}

interface ControlPlane {
  call: (method: string, params: unknown) => Promise<unknown>
  close: () => void
}

function paneForSession(page: Page, sessionId: string) {
  return page.locator(`.pane[data-session-id="${sessionId}"]`)
}

async function freshClient(browser: Browser, endpoint: BackendEndpoint): Promise<Page> {
  // baseURL explicitly: a context made by hand inherits nothing from the
  // config's `use`, so `goto('/')` would have no origin to resolve against.
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await bindEndpoint(page, endpoint)
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

async function output(
  endpoint: BackendEndpoint,
  live: Pick<LiveSession, 'sessionId' | 'instanceId' | 'sessionEpoch'>,
  from: number,
): Promise<SessionOutput> {
  return ask<SessionOutput>(endpoint, 'session.output', {
    sessionId: live.sessionId,
    instanceId: live.instanceId,
    sessionEpoch: live.sessionEpoch,
    from,
  })
}

/** Read every recorded run, advancing over explicit gaps as well as bytes.
 *  The gaps are collected rather than stepped over in silence: they are half
 *  of what this test is here to assert. */
async function recordedText(
  endpoint: BackendEndpoint,
  live: Pick<LiveSession, 'sessionId' | 'instanceId' | 'sessionEpoch'>,
): Promise<{ text: string; produced: number; gaps: SessionOutput['gaps'] }> {
  let text = ''
  let at = 0
  const seen = new Set<string>()
  const gaps: SessionOutput['gaps'] = []
  for (;;) {
    const page = await output(endpoint, live, at)
    let cursor = at
    for (const gap of page.gaps) {
      // Paging re-reports a gap that straddles the cursor, and a duplicate
      // would make "how many holes" a property of the page size.
      const key = `${gap.start}:${gap.end}:${gap.reason}`
      if (!seen.has(key)) {
        seen.add(key)
        gaps.push(gap)
      }
      cursor = Math.max(cursor, gap.end)
    }
    for (const run of page.runs) {
      const bytes = Buffer.from(run.body, 'base64')
      text += bytes.toString('utf8')
      cursor = Math.max(cursor, run.offset + bytes.length)
    }
    if (cursor <= at || cursor >= page.produced) {
      return { text, produced: page.produced, gaps }
    }
    at = cursor
  }
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
 * POSIX sh, because the pane runs the host's real login shell.
 *
 * WHY THE FILLER IS OVERWRITTEN RATHER THAN PRINTED. It is one burst of `x`
 * ending in a carriage return, so it costs the megabytes the helper's window
 * is measured in without putting tens of thousands of rows into the browser.
 * The bytes are what the window counts, and they are real.
 *
 * WHY `exit` AND NOT A SUBSHELL. The status this bead cares about is the
 * SESSION's — the one the helper reports and `ExitOutcome` maps — so the shell
 * itself has to end. A subshell would exercise shell integration instead,
 * which is a different owner answering a different question.
 */
function longRemoteCommand(gates: RemoteGates, fillerBytes: number): string {
  const marker = shellQuote(gates.markerFile)
  const detached = shellQuote(gates.detached)
  const fillerDone = shellQuote(gates.fillerDone)
  const finish = shellQuote(gates.finish)
  return [
    'i=0',
    `while [ ! -e ${finish} ]; do printf '${MARKER_PREFIX}-%03d\\n' "$i" >> ${marker}`,
    `printf '${MARKER_PREFIX}-%03d\\n' "$i"`,
    `if [ "$i" -eq 0 ]; then while [ ! -e ${detached} ]; do sleep 1; done`,
    `head -c ${fillerBytes} /dev/zero | tr '\\0' x; printf '\\r'; : > ${fillerDone}; fi`,
    'i=$((i+1)); sleep 1; done',
    `exit ${EXIT_CODE}`,
  ].join('; ')
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

/** The shipped install gesture: the Git panel on a connected remote tab asks
 *  for consent, and accepting it puts the helper on the host. The branch line
 *  appearing is the panel's own statement that the helper answered. */
async function installHelperThroughProduct(
  page: Page,
  endpoint: BackendEndpoint,
  fixture: SshdFixture,
): Promise<string> {
  const profileName = await createProfileAndOpen(page, endpoint, fixture)
  await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })
  await page.locator(VIEW_GIT).click()
  await expect(page.locator(GIT_PANEL)).toBeVisible({ timeout: 30_000 })
  await expect(page.locator(GIT_CONSENT)).toBeVisible({ timeout: 30_000 })
  await page.locator(GIT_ACCEPT).click()
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
    first = await freshClient(browser, endpoint)
    await promptReady(first)
    const profileName = await installHelperThroughProduct(first, endpoint, fixture)

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

    // ── the build starts ──────────────────────────────────────────────────
    //
    // The filler is sized from the HOST's own window, read out of its
    // inventory a moment ago — never from the coordinator's replay ring,
    // which is a different bound on a different machine.
    const markerCommand = longRemoteCommand(gates, hostBefore.launch.windowBytes + 64 * 1024)
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
    const producedAtDetach = (await recordedText(endpoint, liveBefore)).produced

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
    // empty renderer session map, and a token neither of them has seen. The
    // only route back to this pane is server-side live-session discovery.
    returned = await freshClient(browser, endpoint)
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
    // THIS IS THE ONE ASSERTION THIS SPEC IS STILL RED ON, and `toBeVisible`
    // rather than `toBeAttached` is deliberate. The block IS built and IS in
    // the DOM — `<div class="cmd-block" data-restored="true"
    // data-output-evicted="true">`, resolved 123 times in 60 s — and it is
    // `display: none`, because a re-adopted session has no lifecycle channel
    // (nocx-k6p18.31), so the pane classifies the shell as markerless, enters
    // `unstructured` mode, and `.scrollback-inner.inner-fullscreen-mode >
    // *:not(.xterm-live-container)` hides every block in the stack. A history
    // a person cannot see has not survived anything, so the weaker assertion
    // would be this test agreeing with the defect.
    const returnedBlock = returnedPane
      .locator('.cmd-block')
      .filter({ hasText: MARKER_PREFIX })
      .first()
    await expect(returnedBlock).toBeVisible({ timeout: 60_000 })

    // ── what was lost is NAMED, and named correctly ───────────────────────
    //
    // The card the reclaim raises (recovery-notice.tsx). A pane that came
    // back short and said nothing is the soft degrade AGENTS.md forbids.
    const notice = returnedPane.locator('.nocx-recovery-notice')
    await expect(notice).toBeVisible({ timeout: 60_000 })
    await expect(notice).toContainText(/of this session's output is missing/i)
    // And the REASON, in the product's own words and IN ITS OWN CLAUSE. The
    // card accounts for the hole one clause per owner (recovery-notice.tsx
    // `clauses`), so the word appearing somewhere in the sentence is not the
    // same fact as the reason being named: `cap` bytes are reported as "the
    // recording's size limit dropped", and only a reason this build has no
    // sentence for reaches the reader as the wire's own word. Asserting the
    // CLAUSE is what makes a host-window hole filed under some other reason
    // fail here, which is the whole point — a sentence that sent a person to
    // a retention knob would be sending them to one that did nothing about
    // the execution host's window.
    //
    // MEASURED (2026-09-02): recording the hole as `unrecorded` in
    // ws_session_record.go's sessionOutputHoleReason turns this clause into
    // "96.5 kB that was never recorded" and this line goes red.
    //
    // A BARE `not.toContainText('size limit')` STOOD HERE AND WAS WRONG, and
    // the run that would have failed on it is the run where everything works.
    // This scenario produces TWO holes and both are correctly named: the
    // host's window reclaimed ~96 kB that no coordinator could ever have
    // received, and the fresh coordinator's OWN retention then dropped ~3.9 MB
    // of the 4 MiB replay it did receive — measured as
    // `[{1797..98304 hostWindow}, {131072..4046848 cap}]`. A card that says
    // both is the card being honest about two different owners, so a blanket
    // negative could only ever fail the product for telling the truth.
    await expect(notice).toContainText(/missing as "hostWindow"/)

    // The same fact off the coordinator's own method, where the reason is a
    // field rather than a sentence.
    const finalOutput = await recordedText(endpoint, liveAfter!)
    expect(finalOutput.gaps.length).toBeGreaterThan(0)
    const hostWindowGaps = finalOutput.gaps.filter((gap) => gap.reason === 'hostWindow')
    expect(hostWindowGaps.length).toBeGreaterThan(0)
    expect(hostWindowGaps.some((gap) => gap.end - gap.start > 0)).toBe(true)
    expect(finalOutput.produced).toBeGreaterThan(producedAtDetach)

    // The markers the recording DOES hold are one process's output in stream
    // order — not two runs spliced together at the reclaim.
    const finalMarkers = markersIn(finalOutput.text)
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

    // And the host agrees, from its own record of the process it owned.
    await expect
      .poll(
        async () =>
          helperSession(await inventory(endpoint), liveBefore.sessionId)?.exit?.code ?? null,
        { timeout: 60_000, message: 'the helper never reported the build a real exit status' },
      )
      .toBe(EXIT_CODE)

    console.log(
      `remote reclaim: windowBytes=${hostBefore.launch.windowBytes} detachedBytes=${detachedBytes} ` +
        `markersAtDetach=${markerAtDetach} markersBeforeReturn=${markerBeforeReturn} ` +
        `gaps=${JSON.stringify(finalOutput.gaps)}`,
    )
  } finally {
    if (returned)
      await returned
        .context()
        .close()
        .catch(() => undefined)
    if (first)
      await first
        .context()
        .close()
        .catch(() => undefined)
    backend.stop()
    fixture?.proc.kill('SIGKILL')
    rmSync(remoteRoot, { recursive: true, force: true })
    rmSync(backendRoot.root, { recursive: true, force: true })
  }
})
