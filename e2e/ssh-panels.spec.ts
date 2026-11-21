// e2e: the far helper's axis on the pane head and the Git panel, under the
// faults a connection can meet (ADR-727g63tr decision 2; design §5.10;
// nocx-oalo1.6.3c-1).
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
// # No timing
//
// Every wait is on an observable: the head's own data-helper attribute, the
// Git branch badge, the destroyed sockets the fault proxy reports, the far
// host's own HELPERKILLDONE line, a redial's own account on the far side, and
// a command's output. Nothing sleeps for an effect to arrive. The kill
// scenario additionally HOLDS the backend's automatic redial behind a
// far-side gate, so the return is a moment this spec chooses rather than a
// speed its assertions have to beat.
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
import { readStand } from './stand'

const TAB = '.nocx-tab'
const EDITOR = '.pane.active .nocx-editor-input'
const HEAD = '[data-testid="connection-head"]'
const HEAD_TEXT = '[data-testid="connection-head"] .ui-connection-head__text'
const HEAD_ACTION = '[data-testid="connection-head"] .ui-connection-head__action'
const GIT_AXIS = '[data-testid="git-helper-axis"]'
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
    ...(killDir === null ? {} : { args: ['-helper-kill-dir', killDir] }),
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
