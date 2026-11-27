// e2e: the far helper's axis on the pane head, the Git panel and the Files
// panel, under the faults a connection can meet (ADR-727g63tr decision 2;
// design §5.10; nocx-oalo1.6.3c-1, nocx-oalo1.12.8).
//
// # What a person can do that they could not before
//
// Open an SSH tab and see, on the pane's own head and in the Git panel, the
// ONE answer to "does the nocx helper serve this connection": nothing while it
// serves, a quiet preparation label while it starts, the reason and the one
// backend-offered action when it does not — and a repository that opens by
// itself when the helper comes back, without pressing anything.
//
// # Why four faults, and what each one proves
//
//   serving          the repository is opened through the helper, and the
//                    head carries no reason at all.
//   raw host         the backend's refusal is the SAME sentence on both
//                    surfaces, with Host settings and no Retry / no install.
//   a cut            the axis turns paused, and the return reaches serving and
//                    a repository again with nobody pressing anything.
//   the helper dies  helper-lost with Refresh while the SHELL still runs;
//                    Refresh redials and the repository returns, and nothing
//                    is installed a second time.
//
// # The Files half of the same axis (nocx-oalo1.12.8)
//
//   serving          Files lists the far host, opens a file's bytes, uploads a
//                    drop and downloads the same bytes back — compared on the
//                    far side, which is the only proof a transfer happened.
//   raw host         the SAME sentence the head draws, Host settings, no
//                    Retry, no wire token and no "could not reach files".
//   a cut            the panel pauses with the head's sentence and comes back
//                    by itself, with the folders the person opened still open.
//   helper killed    Refresh on the panel's own card restores Files.
//   an unreadable directory  the row shows the refusal while `data-helper`
//                    stays `serving`: a listing error is not an axis change.
//
// # No timing
//
// Every wait is on an observable: the head's own data-helper attribute, the
// Git branch badge, the destroyed sockets the fault proxy reports, the far
// host's own HELPERKILLDONE line, a redial's own account on the far side, and
// a command's output. Nothing sleeps for an effect to arrive. The kill
// scenario additionally HOLDS the backend's automatic redial behind a
// far-side gate, so the return is a moment this spec chooses rather than a
// speed its assertions have to beat.
import { createHash, randomBytes } from 'node:crypto'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import {
  appReadyForInput,
  expect,
  resolveBackend,
  showSidebarView,
  test,
  type Page,
} from './harness'
import { dropFileOnActivePane } from './drop-gesture'
import { killFarHelper } from './far-helper-faults'
import { startFaultProxy, type FaultProxy } from './fault-proxy'
import { createRepo, type GitRepo } from './git-fixture'
import {
  hostKeyFingerprint,
  recordHostMethod,
  rpc,
  startSshd,
  type SshdFixture,
} from './sshd-fixture'

const TAB = '.nocx-tab'
const EDITOR = '.pane.active .nocx-editor-input'
const HEAD = '[data-testid="connection-head"]'
const HEAD_TEXT = '[data-testid="connection-head"] .ui-connection-head__text'
const HEAD_ACTION = '[data-testid="connection-head"] .ui-connection-head__action'
const GIT_AXIS = '[data-testid="git-helper-axis"]'
const FILES_PANEL = '[data-testid="files-panel"]'
const FILES_AXIS = '[data-testid="files-helper-axis"]'
const FILES_ROW = '.files-row'
const TREE_NAME = '.ui-tree-row__name'
const GIT_BRANCH = '[data-testid="git-branch"]'
const GIT_PANEL = '[data-testid="git-panel"]'

/** The one fold's sentences, in the words a person reads (helper-axis.ts).
 *  Hardcoded on purpose: the spec is the independent witness that both
 *  surfaces draw the same text, and that no wire token leaks into it. */
const RAW_SENTENCE = 'This host uses Raw integration, so the nocx helper is not used.'
const LOST_SENTENCE = 'The nocx helper stopped on this connection.'
const PAUSED_SENTENCE = 'The connection is paused, so the nocx helper is not reachable.'

/** The disposable stand home the backend was launched with — where its ssh
 *  client reads known_hosts from. */
const STAND_HOME = process.env.NOCX_E2E_HOME_DIR || path.resolve(__dirname, '..', '.e2e', 'home')

/** The far host's key, trusted in the backend's home. REPLACED, never
 *  appended: every fixture spawn mints a fresh key, and a stale line for a
 *  dead one makes the backend refuse the connection. */
function trustHostKey(knownHosts: string): void {
  const sshDir = path.join(STAND_HOME, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), knownHosts + '\n')
}

/** The fixture's own known_hosts line, spelled for the port the profile
 *  dials — the relay's when one stands in front of the host. */
function knownHostsFor(fixture: SshdFixture, port: number): string {
  const rest = fixture.knownHosts.slice(fixture.knownHosts.indexOf(' ') + 1)
  return `[127.0.0.1]:${port} ${rest}`
}

interface Host {
  fixture: SshdFixture
  remoteHome: string
  repo: GitRepo
  killDir: string | null
}

/** A far host whose HOME is disposable and whose shell starts inside a real
 *  git repository. `-helper-kill-dir` is passed only by the scenario that
 *  kills the daemon: a run that kills nothing watches nothing. */
async function startHost(killable = false): Promise<Host> {
  // THE HOME IS THE REPOSITORY, deliberately. A helper-hosted shell is spawned
  // by the far helper's own pty, whose resolveCwd falls back to the login
  // user's home when no cwd is given (cmd/e2e-sshd's seedRepo comment), so a
  // home that is not the checkout would leave every Git scenario looking at
  // an empty directory. The helper's own `.nocx` tree is excluded from git so
  // it cannot show up as a change.
  const repo = createRepo()
  writeFileSync(path.join(repo.root, '.git', 'info', 'exclude'), '.nocx/\n', { flag: 'a' })
  const remoteHome = repo.root
  const killDir = killable ? mkdtempSync(path.join(tmpdir(), 'nocx-ssh-panels-kill-')) : null
  const fixture = await startSshd({
    home: remoteHome,
    cwd: repo.root,
    ...(killDir === null ? {} : { args: ['-helper-kill-dir', killDir, '-shell', 'zsh'] }),
  })
  return { fixture, remoteHome, repo, killDir }
}

async function stopHost(host: Host): Promise<void> {
  const proc = host.fixture.proc
  if (proc.exitCode === null && proc.signalCode === null) {
    const exited = new Promise<void>((resolve) => proc.once('exit', () => resolve()))
    proc.kill('SIGKILL')
    await exited
  }
  // The far helper is this machine's process too: between the daemon's death
  // and the fixture's, one can still be writing its `.nocx` tree under the
  // home, which is the checkout. Retries are the difference between a cleanup
  // that reports the product and one that reports itself.
  rmSync(host.repo.root, { recursive: true, force: true, maxRetries: 20, retryDelay: 100 })
  if (host.killDir !== null) rmSync(host.killDir, { recursive: true, force: true })
}

/** Open a saved connection through the palette and wait for the SAVED row
 *  before Enter — an empty list's fallback row contains every profile name,
 *  and Enter on it dials the name as a host (nocx-k1691). */
async function openSavedProfile(page: Page, name: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible()
  await search.fill(name)
  const row = page.locator('.quick-connect__item:not(.quick-connect__item--system)', {
    hasText: name,
  })
  await expect(row).toBeVisible({ timeout: 10_000 })
  await expect(row).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
}

/** The shell's prompt, for a raw connection: the grid takes the keys and no
 *  editor comes up. */
async function waitForPrompt(page: Page): Promise<void> {
  await expect
    .poll(
      async () =>
        page.evaluate(() =>
          Array.from(document.querySelectorAll('.pane.active .term-grid-row')).some((row) =>
            /[$#]\s*$/.test((row.textContent ?? '').trimEnd()),
          ),
        ),
      { timeout: 60_000 },
    )
    .toBe(true)
}

/** Wait until the Files panel is showing the TAB's own directory: rooted at
 *  `/`, with the tab's verified cwd revealed — the row selected and open,
 *  which is what puts the far host's entries on screen. The reveal is the
 *  renderer's own account (`data-selected`, `data-disclosure`), and it is
 *  only set for a cwd the shell reported. */
async function openFilesOnTab(page: Page, remoteRoot: string): Promise<void> {
  await showSidebarView(page, 'files')
  const panel = page.locator(FILES_PANEL)
  await expect(panel).toBeVisible({ timeout: 15_000 })
  await expect(panel).toHaveAttribute('data-root', '/', { timeout: 30_000 })
  const rootRow = page.locator(`${FILES_ROW}[data-path="${remoteRoot}"]`).getByRole('treeitem')
  await expect(rootRow).toHaveAttribute('data-selected', 'true', { timeout: 60_000 })
  await expect(rootRow).toHaveAttribute('data-disclosure', 'expanded', { timeout: 30_000 })
}

/** One folder of the far host, created before the panel opens so the first
 *  listing already holds it. */
function seedRemoteDir(host: Host, name: string, file = 'inside.txt'): string {
  const dir = path.join(host.repo.root, name)
  mkdirSync(dir, { recursive: true })
  writeFileSync(path.join(dir, file), 'inside\n')
  return dir
}

interface Wired {
  host: Host
  profileId: string
  profileName: string
  /** The port the profile dials: the fixture's, or its relay's. */
  port: number
  proxy: FaultProxy | null
}

/** Trust a fixture (optionally behind a relay), record its host method, seed a
 *  profile and bring the app up. `mode` is written through the connect-time
 *  ask's own backend call, so `helper` also records the machine's grant. */
async function wire(
  page: Page,
  opts: { killable?: boolean; throughProxy?: boolean; mode: 'raw' | 'script' | 'helper' },
): Promise<Wired> {
  const host = await startHost(opts.killable)
  const proxy = opts.throughProxy
    ? await startFaultProxy(host.fixture.host, host.fixture.port)
    : null
  const port = proxy === null ? host.fixture.port : proxy.port
  trustHostKey(knownHostsFor(host.fixture, port))

  await page.goto('/')
  await appReadyForInput(page)
  await expect(page.locator(TAB)).toHaveCount(1)

  const endpoint = await resolveBackend(page)
  const profileName = `e2e-ssh-panels-${Date.now()}-${opts.mode}`
  const created = await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
    type: 'ssh',
    name: profileName,
    options: {
      host: '127.0.0.1',
      port,
      user: host.fixture.user,
      keyPath: host.fixture.userKey,
    },
  })
  await recordHostMethod(page, endpoint, hostKeyFingerprint(host.fixture.knownHosts), opts.mode)
  return { host, profileId: created.id, profileName, port, proxy }
}

async function unwire(page: Page, wired: Wired): Promise<void> {
  try {
    const endpoint = await resolveBackend(page)
    await rpc(page, endpoint, 'profiles.delete', { id: wired.profileId })
  } catch {
    // A cleanup that throws would replace the real failure with its own.
  }
  if (wired.proxy !== null) await wired.proxy.close().catch(() => undefined)
  await stopHost(wired.host)
}

/** The helper's installs on the far host: one directory per generation. */
function helperGenerations(remoteHome: string): number {
  const root = path.join(remoteHome, '.nocx', 'helper')
  return existsSync(root) ? readdirSync(root).length : 0
}

/** The far helper's automatic redial, held at a gate this spec owns.
 *
 * WHY. The backend redials a lost helper by itself, a second after the loss,
 * and a healthy return removes the Refresh button this scenario presses. No
 * longer wait fixes that — it widens the race instead of closing it. So the
 * redial is made to PARK: the installed binary is swapped for a wrapper that
 * announces the dial and waits for a file only the spec writes. The
 * automatic return becomes a moment the spec chooses rather than a speed it
 * hopes to beat.
 *
 * The wrapper execs the real binary it was installed over, so the bridge
 * speaks to the same helper; the directory, its name and its generation are
 * untouched, which is what keeps the "redial, never reinstall" reading
 * honest. Both files live under the home's `.nocx/` tree, which the fixture
 * excludes from the git checkout standing in the same directory.
 */
interface RedialHold {
  /** How many dials the far side has started since the hold was installed. */
  dials(): number
  /** Let the parked dial proceed. */
  release(): void
  /** Put the install back the way the product left it. */
  restore(): void
}

function holdFarRedial(remoteHome: string): RedialHold {
  const root = path.join(remoteHome, '.nocx', 'helper')
  const dir = readdirSync(root)
    .map((name) => path.join(root, name))
    .find((candidate) => existsSync(path.join(candidate, 'nocx-helper')))
  if (dir === undefined) throw new Error(`holdFarRedial: no helper install under ${root}`)
  const binary = path.join(dir, 'nocx-helper')
  const real = path.join(dir, 'nocx-helper.real')
  const log = path.join(remoteHome, '.nocx', 'redial.log')
  const gate = path.join(remoteHome, '.nocx', 'redial-go')
  renameSync(binary, real)
  rmSync(log, { force: true })
  rmSync(gate, { force: true })
  writeFileSync(
    binary,
    [
      '#!/bin/sh',
      `printf '%s\\n' "DIAL $$" >> '${log}'`,
      `while [ ! -f '${gate}' ]; do sleep 0.05; done`,
      `exec '${real}' "$@"`,
      '',
    ].join('\n'),
    { mode: 0o755 },
  )
  return {
    dials: () =>
      existsSync(log)
        ? readFileSync(log, 'utf8')
            .split('\n')
            .filter((line) => line !== '').length
        : 0,
    release: () => writeFileSync(gate, 'go\n'),
    restore: () => {
      rmSync(binary, { force: true })
      renameSync(real, binary)
    },
  }
}

/** One captured WebSocket frame, in the order it crossed the socket. */
interface WireFrame {
  readonly direction: 'sent' | 'received'
  readonly payload: string
}

interface RpcFrame {
  readonly id?: unknown
  readonly method?: unknown
  readonly params?: unknown
  readonly result?: unknown
  readonly error?: unknown
}

function parseRpcFrame(payload: string): RpcFrame | null {
  try {
    const value: unknown = JSON.parse(payload)
    return typeof value === 'object' && value !== null ? (value as RpcFrame) : null
  } catch {
    return null
  }
}

function recordOrNull(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null
}

test('a serving helper opens the repository and says nothing on the head', async ({ page }) => {
  test.setTimeout(180_000)
  const wired = await wire(page, { mode: 'helper' })
  try {
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 90_000 })
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', { timeout: 30_000 })

    await showSidebarView(page, 'git')
    await expect(page.locator(GIT_BRANCH)).toBeVisible({ timeout: 60_000 })
    await expect(page.locator(GIT_BRANCH)).toHaveText('main')

    // Nothing to explain while it serves: no sentence and no axis button.
    await expect(page.locator(HEAD_TEXT)).not.toContainText('nocx helper')
    await expect(page.locator(`${HEAD_ACTION} .ui-button`)).toHaveCount(0)
  } finally {
    await unwire(page, wired)
  }
})

test('a raw host draws the same reason and Host settings on the head and in Git, with no Retry', async ({
  page,
}) => {
  test.setTimeout(180_000)
  const wired = await wire(page, { mode: 'raw' })
  try {
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 60_000 })
    await waitForPrompt(page)

    const head = page.locator(HEAD)
    await expect(head).toHaveAttribute('data-helper', 'unavailable', { timeout: 30_000 })
    await expect(page.locator(HEAD_TEXT)).toContainText(RAW_SENTENCE)
    await expect(page.locator(HEAD_ACTION)).toContainText('Host settings')

    await showSidebarView(page, 'git')
    const axis = page.locator(GIT_AXIS)
    await expect(axis).toBeVisible({ timeout: 30_000 })
    await expect(axis).toContainText(RAW_SENTENCE)
    await expect(page.locator('[data-testid="git-host-settings"]')).toBeVisible()
    await expect(page.locator('[data-testid="git-error"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="git-retry-open"]')).toHaveCount(0)
    await expect(page.locator(GIT_PANEL)).not.toContainText('Could not reach git')
    await expect(page.locator(GIT_PANEL)).not.toContainText('mode-raw')

    const labels = await page.locator(`${GIT_PANEL} button`).allTextContents()
    for (const label of labels) expect(label).not.toMatch(/accept|allow|install|use the helper/i)

    // Host settings is a door, not a word: it opens the saved connection's
    // editor on the field that decides the host's method.
    await page.locator('[data-testid="git-host-settings"]').click()
    const editor = page.getByRole('dialog').filter({ hasText: 'Edit Connection' })
    await expect(editor).toBeVisible({ timeout: 20_000 })
    await expect(page.locator('#profile-integration-method')).toBeVisible({ timeout: 20_000 })
  } finally {
    await unwire(page, wired)
  }
})

test('a cut pauses the axis, and the return opens the repository again with no press', async ({
  page,
}) => {
  test.setTimeout(240_000)
  const wired = await wire(page, { mode: 'helper', throughProxy: true })
  try {
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 90_000 })
    await showSidebarView(page, 'git')
    await expect(page.locator(GIT_BRANCH)).toBeVisible({ timeout: 60_000 })

    // The paused state is a STATE, not a duration: an automatic reconnect can
    // carry the axis back to serving before a second locator assertion runs.
    // Record every mutation of the head and the Git panel, so the pause is
    // evidence rather than a race with the return.
    await page.evaluate(() => {
      type Record_ = { head: string | null; headText: string; git: string | null; gitText: string }
      const record = window as unknown as { __axis: Record_[] }
      record.__axis = []
      const snap = (): void => {
        const head = document.querySelector('[data-testid="connection-head"]')
        const git = document.querySelector('[data-testid="git-helper-axis"]')
        record.__axis.push({
          head: head?.getAttribute('data-helper') ?? null,
          headText: head?.querySelector('.ui-connection-head__text')?.textContent ?? '',
          git: git?.getAttribute('data-helper') ?? null,
          gitText: git?.textContent ?? '',
        })
      }
      snap()
      const target = document.querySelector('[data-testid="git-panel"]') ?? document.body
      new MutationObserver(snap).observe(target, {
        subtree: true,
        attributes: true,
        characterData: true,
        childList: true,
      })
      new MutationObserver(snap).observe(
        document.querySelector('[data-testid="connection-head"]') ?? document.body,
        { attributes: true, subtree: true, characterData: true, childList: true },
      )
    })

    wired.proxy!.cut()

    // The backend redials by itself (ssh.reconnect defaults to auto). Nothing
    // in this spec presses anything.
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', {
      timeout: 120_000,
    })
    await expect(page.locator(GIT_BRANCH)).toBeVisible({ timeout: 60_000 })
    await expect(page.locator(GIT_BRANCH)).toHaveText('main')

    const seen = await page.evaluate(
      () => (window as unknown as { __axis: Array<Record<string, string | null>> }).__axis,
    )
    expect(
      seen.some((r) => r.head === 'paused' && String(r.headText).includes(PAUSED_SENTENCE)),
      `head never showed the pause: ${JSON.stringify(seen.slice(-20))}`,
    ).toBe(true)
    expect(
      seen.some((r) => r.git === 'paused' && String(r.gitText).includes(PAUSED_SENTENCE)),
      `Git never showed the pause: ${JSON.stringify(seen.slice(-20))}`,
    ).toBe(true)
  } finally {
    await unwire(page, wired)
  }
})

test('a helper killed with the transport up offers Refresh on both surfaces, and Refresh restores Git without installing again', async ({
  page,
}) => {
  test.setTimeout(240_000)
  // THE WIRE IS THE WITNESS THAT THE PRESS REACHED THE BACKEND. The socket
  // exists only for newer connections, so the listener is installed before
  // the app is brought up — registering it beside the click would capture
  // nothing.
  const frames: WireFrame[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (e) => {
      if (typeof e.payload === 'string') frames.push({ direction: 'sent', payload: e.payload })
    })
    ws.on('framereceived', (e) => {
      if (typeof e.payload === 'string') frames.push({ direction: 'received', payload: e.payload })
    })
  })
  // THE SHELL IS DELIBERATELY NOT HELPER-HOSTED. A mode change applies to a
  // host's connections at once, while a shell already open keeps the
  // integration it opened with (ADR-727g63tr decision 1): opening by Script
  // and then granting the helper starts the far helper FOR THE PANELS on the
  // live connection, and killing that daemon leaves the script-integrated
  // shell alone. Opening by Helper would kill the shell with its host, and
  // the scenario would prove nothing about an untouched shell.
  const wired = await wire(page, { mode: 'script', killable: true })
  try {
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 90_000 })
    const endpoint = await resolveBackend(page)
    await recordHostMethod(
      page,
      endpoint,
      hostKeyFingerprint(wired.host.fixture.knownHosts),
      'helper',
    )
    await showSidebarView(page, 'git')
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', {
      timeout: 90_000,
    })
    await expect(page.locator(GIT_BRANCH)).toBeVisible({ timeout: 60_000 })
    const installsBefore = helperGenerations(wired.host.remoteHome)
    expect(installsBefore).toBeGreaterThan(0)

    // THE AUTOMATIC RETURN IS HELD, NOT RACED. Installed before the death so
    // the very first redial parks; a return can now happen only when this
    // spec releases it, so the shell step below cannot lose the button the
    // scenario presses.
    const redial = holdFarRedial(wired.host.remoteHome)
    try {
      await killFarHelper(wired.host.fixture)

      // The far side's own account that the redial has started and parked.
      await expect.poll(() => redial.dials(), { timeout: 30_000 }).toBeGreaterThan(0)

      await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'unavailable', {
        timeout: 30_000,
      })
      await expect(page.locator(HEAD_TEXT)).toContainText(LOST_SENTENCE)
      await expect(page.locator(HEAD_ACTION)).toContainText('Refresh')
      await expect(page.locator(GIT_AXIS)).toContainText(LOST_SENTENCE)
      await expect(page.locator('[data-testid="git-helper-refresh"]')).toBeVisible()

      // THE SHELL IS UNTOUCHED: a command typed now runs and prints. The
      // command text does not contain the marker, so the marker can only be
      // output. The helper's return is parked, so this step cannot cost the
      // button below its existence.
      await page.locator(EDITOR).click()
      await page.keyboard.type("printf 'NOCXPANELALIVE\\n'")
      await page.keyboard.press('Enter')
      await expect(page.locator('.pane.active')).toContainText('NOCXPANELALIVE', {
        timeout: 30_000,
      })

      // THE PERSON'S PRESS, while the automatic return is still parked.
      await page.locator('[data-testid="git-helper-refresh"]').click()

      // THE GATE COMES DOWN ON A CONFIRMED REFRESH, NOT ON A CLICK. Find the
      // request this press sent, check the epoch it carried against the last
      // observation this pane received — a stale epoch is refused by the
      // driver — and only then release the parked dial.
      await expect
        .poll(() =>
          frames.some(
            (f) =>
              f.direction === 'sent' &&
              parseRpcFrame(f.payload)?.method === 'connection.helperRefresh',
          ),
        )
        .toBe(true)
      const pressIndex = frames.findIndex(
        (f) =>
          f.direction === 'sent' && parseRpcFrame(f.payload)?.method === 'connection.helperRefresh',
      )
      const press = parseRpcFrame(frames[pressIndex]!.payload)!
      const params = recordOrNull(press.params)
      const paneId = params?.paneId
      expect(
        typeof paneId,
        `helper refresh carried no paneId: ${frames[pressIndex]!.payload}`,
      ).toBe('string')
      expect(typeof params?.connectionEpoch, 'helper refresh carried no epoch').toBe('number')

      // Ordered frames make "the last observation" exact: a pane.connection
      // received after the request went out cannot be what the press read.
      let observedEpoch: number | null = null
      for (const frame of frames.slice(0, pressIndex)) {
        if (frame.direction !== 'received') continue
        const received = parseRpcFrame(frame.payload)
        if (received?.method !== 'pane.connection') continue
        const receivedParams = recordOrNull(received.params)
        if (receivedParams === null) continue
        if (
          receivedParams.paneId === paneId &&
          typeof receivedParams.connectionEpoch === 'number'
        ) {
          observedEpoch = receivedParams.connectionEpoch
        }
      }
      expect(
        observedEpoch,
        `no pane.connection for ${String(paneId)} before the press`,
      ).not.toBeNull()
      expect(params?.connectionEpoch).toBe(observedEpoch)

      await expect
        .poll(
          () => {
            return frames.some((f) => {
              if (f.direction !== 'received') return false
              const received = parseRpcFrame(f.payload)
              const result = recordOrNull(received?.result)
              return (
                received !== null &&
                received.id === press.id &&
                received.error === undefined &&
                result?.refreshed === true
              )
            })
          },
          { timeout: 30_000 },
        )
        .toBe(true)

      // The moment of the automatic return, CHOSEN here rather than slept
      // for: the parked dial proceeds and the axis comes back.
      redial.release()
      await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', {
        timeout: 60_000,
      })
      await expect(page.locator(GIT_BRANCH)).toBeVisible({ timeout: 60_000 })
      await expect(page.locator(GIT_BRANCH)).toHaveText('main')

      // Redial, never install: the generation directory is the same one.
      expect(helperGenerations(wired.host.remoteHome)).toBe(installsBefore)
      const labels = await page.locator(`${GIT_PANEL} button`).allTextContents()
      for (const label of labels) expect(label).not.toMatch(/accept|allow|install|use the helper/i)
    } finally {
      redial.release()
      redial.restore()
    }
  } finally {
    await unwire(page, wired)
  }
})

// ── The Files panel on the same axis (nocx-oalo1.12.8, C25/C26) ───────────

test('a serving helper lets Files list, open, upload and download on the far host', async ({
  page,
}, testInfo) => {
  test.setTimeout(240_000)
  const wired = await wire(page, { mode: 'helper' })
  try {
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 90_000 })
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', { timeout: 30_000 })
    await openFilesOnTab(page, wired.host.repo.root)

    // LIST: the repository's own tracked file is on screen, and a serving
    // axis says nothing at all — no card, no sentence, no button.
    await expect(page.locator(TREE_NAME).filter({ hasText: 'notes.md' })).toBeVisible({
      timeout: 30_000,
    })
    await expect(page.locator(FILES_AXIS)).toHaveCount(0)

    // UPLOAD: a browser drop on the tab is an upload into its cwd. The row is
    // the panel's account; the BYTES on the far side are the proof.
    const payload = randomBytes(256 * 1024 + 4096)
    const expected = createHash('sha256').update(payload).digest('hex')
    const name = `dropped-${Date.now()}.bin`
    await page.locator(EDITOR).click()
    await dropFileOnActivePane(page, name, payload)
    await expect(page.locator(TREE_NAME).filter({ hasText: name })).toBeVisible({
      timeout: 60_000,
    })
    const onHost = path.join(wired.host.repo.root, name)
    await expect
      .poll(
        () =>
          existsSync(onHost)
            ? createHash('sha256').update(readFileSync(onHost)).digest('hex')
            : null,
        { timeout: 30_000 },
      )
      .toBe(expected)

    // DOWNLOAD: the row's menu hands the same file back to the browser, and
    // the saved bytes are compared with the ones that went up.
    await page.locator(FILES_ROW).filter({ hasText: name }).click({ button: 'right' })
    const menu = page.locator('[data-testid="files-context-menu"]')
    await expect(menu).toBeVisible({ timeout: 10_000 })
    const arrived = page.waitForEvent('download')
    await menu.getByRole('menuitem', { name: 'Download' }).click()
    const download = await arrived
    const saved = testInfo.outputPath(name)
    await download.saveAs(saved)
    expect(createHash('sha256').update(readFileSync(saved)).digest('hex')).toBe(expected)

    // OPEN: a file row opens a viewer tab holding the far host's bytes.
    await page.locator(FILES_ROW).filter({ hasText: 'notes.md' }).click()
    await expect(page.locator('.file-viewer__editor')).toContainText('hello', {
      timeout: 30_000,
    })
  } finally {
    await unwire(page, wired)
  }
})

test('a raw host draws the same reason in Files as on the head, with Host settings and no Retry', async ({
  page,
}) => {
  test.setTimeout(180_000)
  const wired = await wire(page, { mode: 'raw' })
  try {
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 60_000 })
    await waitForPrompt(page)
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'unavailable', {
      timeout: 30_000,
    })
    await expect(page.locator(HEAD_TEXT)).toContainText(RAW_SENTENCE)

    await showSidebarView(page, 'files')
    const axis = page.locator(FILES_AXIS)
    await expect(axis).toBeVisible({ timeout: 30_000 })
    await expect(axis).toContainText(RAW_SENTENCE)
    await expect(page.locator('[data-testid="files-host-settings"]')).toBeVisible()
    // No doorway the backend did not name, and no local failure dressed as
    // one: the panel's own Retry belongs to a refused files.open, and there
    // was no files.open.
    await expect(page.locator('[data-testid="files-helper-refresh"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="files-error"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="files-retry-open"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="files-loading"]')).toHaveCount(0)
    await expect(page.locator('.files-tree')).toHaveCount(0)
    // The wire's own vocabulary stays on the wire.
    await expect(page.locator(FILES_PANEL)).not.toContainText('mode-raw')
    await expect(axis).not.toContainText('mode-raw')

    // Host settings is a door: it opens the saved connection's editor on the
    // field that decides the host's method.
    await page.locator('[data-testid="files-host-settings"]').click()
    const editor = page.getByRole('dialog').filter({ hasText: 'Edit Connection' })
    await expect(editor).toBeVisible({ timeout: 20_000 })
    await expect(page.locator('#profile-integration-method')).toBeVisible({ timeout: 20_000 })
  } finally {
    await unwire(page, wired)
  }
})

test('a cut pauses the Files panel, and the return lists again with the opened folders kept', async ({
  page,
}) => {
  test.setTimeout(240_000)
  const observations = new Set<string>()
  page.on('websocket', (socket) => {
    socket.on('framereceived', ({ payload }) => {
      if (typeof payload !== 'string') return
      const message = parseRpcFrame(payload)
      if (message?.method !== 'pane.connection') return
      const observation = recordOrNull(message.params)
      const helper = recordOrNull(observation?.helper)
      observations.add(
        JSON.stringify([observation?.connectionId, observation?.sessionId, helper?.state]),
      )
    })
  })
  const wired = await wire(page, { mode: 'helper', throughProxy: true })
  try {
    const kept = seedRemoteDir(wired.host, 'kept')
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 90_000 })
    await openFilesOnTab(page, wired.host.repo.root)

    // A folder the person opened by hand: it is what must survive the pause.
    const keptRow = page.locator(`${FILES_ROW}[data-path="${kept}"]`).getByRole('treeitem')
    await keptRow.locator('.ui-tree-row__disclosure').click()
    await expect(keptRow).toHaveAttribute('data-disclosure', 'expanded')
    await expect(page.locator(TREE_NAME).filter({ hasText: 'inside.txt' })).toBeVisible()

    // The pause is a STATE, not a duration: an automatic reconnect can carry
    // the axis back before a second assertion runs, so every mutation of the
    // panel and the head is recorded and the pause is read out of that record.
    await page.evaluate(() => {
      type Record_ = { head: string | null; files: string | null; filesText: string }
      const record = window as unknown as { __filesAxis: Record_[] }
      record.__filesAxis = []
      const snap = (): void => {
        const head = document.querySelector('[data-testid="connection-head"]')
        const files = document.querySelector('[data-testid="files-helper-axis"]')
        record.__filesAxis.push({
          head: head?.getAttribute('data-helper') ?? null,
          files: files?.getAttribute('data-helper') ?? null,
          filesText: files?.textContent ?? '',
        })
      }
      snap()
      const panel = document.querySelector('[data-testid="files-panel"]') ?? document.body
      new MutationObserver(snap).observe(panel, {
        subtree: true,
        attributes: true,
        characterData: true,
        childList: true,
      })
      new MutationObserver(snap).observe(
        document.querySelector('[data-testid="connection-head"]') ?? document.body,
        { attributes: true, subtree: true, characterData: true, childList: true },
      )
    })

    wired.proxy!.cut()

    // THE HOST CHANGES WHILE NOBODY CAN REACH IT. A tree that merely redraws
    // what it already had would pass a "the panel came back" assertion without
    // ever having read the host — so a file that did not exist before the cut
    // is the witness that the return RE-READ, and not just un-blurred an old
    // listing (ALX-1793 round 1, M3).
    const afterCut = path.join(kept, 'after-cut.txt')
    writeFileSync(afterCut, 'made during the cut\n')

    // The backend redials by itself. NOTHING in this spec presses anything.
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', {
      timeout: 120_000,
    })

    // And the panel comes back by itself: the folder the person opened is
    // still open, with the rows the host holds NOW — including the file made
    // while the connection was down.
    await expect(
      keptRow,
      `Connection observations: ${[...observations].join(', ')}`,
    ).toHaveAttribute('data-disclosure', 'expanded', { timeout: 60_000 })
    await expect(page.locator(TREE_NAME).filter({ hasText: 'inside.txt' })).toBeVisible({
      timeout: 60_000,
    })
    await expect(page.locator(`${FILES_ROW}[data-path="${afterCut}"]`)).toHaveCount(1, {
      timeout: 60_000,
    })

    const seen = await page.evaluate(
      () =>
        (window as unknown as { __filesAxis: Array<Record<string, string | null>> }).__filesAxis,
    )
    expect(
      seen.some((r) => r.files === 'paused' && String(r.filesText).includes(PAUSED_SENTENCE)),
      `Files never showed the pause: ${JSON.stringify(seen.slice(-20))}`,
    ).toBe(true)
  } finally {
    await unwire(page, wired)
  }
})

test('a helper killed with the transport up offers Refresh on the Files card, and Refresh restores the panel', async ({
  page,
}) => {
  test.setTimeout(240_000)
  // The wire is the witness that the press reached the backend: the socket
  // exists only for newer connections, so the listener is installed before
  // the app is brought up.
  const frames: WireFrame[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (e) => {
      if (typeof e.payload === 'string') frames.push({ direction: 'sent', payload: e.payload })
    })
    ws.on('framereceived', (e) => {
      if (typeof e.payload === 'string') frames.push({ direction: 'received', payload: e.payload })
    })
  })
  // The SHELL is deliberately script-integrated: killing the helper must not
  // take the shell with it, which is the whole point of the scenario.
  const wired = await wire(page, { mode: 'script', killable: true })
  try {
    const kept = seedRemoteDir(wired.host, 'kept')
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 90_000 })
    const endpoint = await resolveBackend(page)
    await recordHostMethod(
      page,
      endpoint,
      hostKeyFingerprint(wired.host.fixture.knownHosts),
      'helper',
    )
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', {
      timeout: 90_000,
    })
    await openFilesOnTab(page, wired.host.repo.root)
    const installsBefore = helperGenerations(wired.host.remoteHome)
    expect(installsBefore).toBeGreaterThan(0)

    // The automatic return is held, not raced: a healthy redial would remove
    // the button this scenario presses.
    const redial = holdFarRedial(wired.host.remoteHome)
    try {
      await killFarHelper(wired.host.fixture)
      await expect.poll(() => redial.dials(), { timeout: 30_000 }).toBeGreaterThan(0)

      // A file the host gains while the panel is down: the return must SHOW
      // it, which is what tells a re-read from a redraw (M3).
      const afterRefresh = path.join(wired.host.repo.root, 'after-refresh.txt')
      writeFileSync(afterRefresh, 'made while the helper was gone\n')

      await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'unavailable', {
        timeout: 30_000,
      })
      await expect(page.locator(FILES_AXIS)).toContainText(LOST_SENTENCE, { timeout: 30_000 })
      const refresh = page.locator('[data-testid="files-helper-refresh"]')
      await expect(refresh).toBeVisible()
      // The panel stops listing: the tree it showed belongs to a host it can
      // no longer reach.
      await expect(page.locator('.files-tree')).toHaveCount(0)

      await refresh.click()

      // The press reached the backend: one connection.helperRefresh, answered.
      await expect
        .poll(
          () =>
            frames.filter(
              (f) =>
                f.direction === 'sent' &&
                parseRpcFrame(f.payload)?.method === 'connection.helperRefresh',
            ).length,
          { timeout: 30_000 },
        )
        .toBe(1)
      await expect
        .poll(
          () =>
            frames.some((f) => {
              if (f.direction !== 'received') return false
              const received = parseRpcFrame(f.payload)
              const result = recordOrNull(received?.result)
              return received?.error === undefined && result?.refreshed === true
            }),
          { timeout: 30_000 },
        )
        .toBe(true)

      // The moment of the automatic return, chosen here rather than slept for.
      redial.release()
      await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving', {
        timeout: 60_000,
      })
      // Files is back by itself, on the machine the axis paused.
      await expect(page.locator(`${FILES_ROW}[data-path="${kept}"]`)).toHaveCount(1, {
        timeout: 60_000,
      })
      await expect(page.locator(TREE_NAME).filter({ hasText: 'notes.md' })).toBeVisible({
        timeout: 60_000,
      })
      // …and it re-read the host rather than redrawing what it had: the file
      // made while the helper was down is on screen.
      await expect(page.locator(`${FILES_ROW}[data-path="${afterRefresh}"]`)).toHaveCount(1, {
        timeout: 60_000,
      })
      // Redial, never install: the generation directory is the same one.
      expect(helperGenerations(wired.host.remoteHome)).toBe(installsBefore)
    } finally {
      redial.release()
      redial.restore()
    }
  } finally {
    await unwire(page, wired)
  }
})

test('a directory that cannot be read shows its refusal while the axis stays serving', async ({
  page,
}) => {
  test.setTimeout(180_000)
  const wired = await wire(page, { mode: 'helper' })
  try {
    // THE REFUSAL IS STAGED AS A TYPE CHANGE rather than a mode change,
    // because the suite's backend runs as root: `chmod 000` denies nothing to
    // root, and a permission case that cannot fail is a test that only passes.
    // The host's own permission-denied answer is covered by the Go acceptance
    // test of the `fs` service (nocx-oalo1.12.2); what this spec is about is
    // the PANEL's rule — a listing error is not an axis change.
    const broken = seedRemoteDir(wired.host, 'not-a-dir')
    await openSavedProfile(page, wired.profileName)
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 90_000 })
    await openFilesOnTab(page, wired.host.repo.root)
    const row = page.locator(`${FILES_ROW}[data-path="${broken}"]`).getByRole('treeitem')
    await expect(row).toHaveAttribute('data-disclosure', 'collapsed', { timeout: 30_000 })

    // The directory becomes a regular file between the listing and the read.
    rmSync(broken, { recursive: true, force: true })
    writeFileSync(broken, 'not a directory\n')

    await row.locator('.ui-tree-row__disclosure').click()
    const refusal = page.locator('[data-testid="files-state-error"]')
    await expect(refusal).toBeVisible({ timeout: 30_000 })
    // A MEANINGFUL refusal, not merely a non-empty box: the panel says what
    // the provider said (a path that is not a directory), and never a wire
    // token or this module's own paraphrase of one (M2 round 1, m2).
    await expect(refusal).toContainText(/not a directory|no such file|permission denied/i)
    await expect(refusal).not.toContainText(
      /mode-raw|mode-script|helper-lost|install-failed|consent-|unsupported-platform|exec-forbidden|version-mismatch|unknown binding/i,
    )
    // The axis never moved: an unreadable directory is a fact about the
    // directory, and the connection is still serving.
    await expect(page.locator(HEAD)).toHaveAttribute('data-helper', 'serving')
    await expect(page.locator(FILES_AXIS)).toHaveCount(0)
  } finally {
    await unwire(page, wired)
  }
})
