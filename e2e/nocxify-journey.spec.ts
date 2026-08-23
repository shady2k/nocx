import { test, expect } from './harness'
import { documentDir } from './harness'
import { readStand } from './stand'
import { spawn, execFileSync } from 'node:child_process'
import {
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import type { ChildProcess } from 'node:child_process'
import type { Page } from './harness'

// ── P11: the epic's acceptance criterion, one run a person could have
// performed (nocx-q3y9). Every earlier package proved its own seam; this is
// the first time the WHOLE journey is watched against a real sshd. The line
// is TYPED with everything explicit — no alias, no config file: on this box
// OpenSSH resolves ~/.ssh/config from the passwd home, never from the
// disposable $HOME (nocx-mlm7 P11 finding), so an alias can never work here.
// The option-bearing form is what a real user may type, and it is exactly
// the option-carrying path nocx-c5az exists for; the options reaching the
// plan are part of what this journey asserts:
//
//   1. a hand-typed `ssh -i <key> -o StrictHostKeyChecking=no -o
//      UserKnownHostsFile=/dev/null -o NumberOfPasswordPrompts=1 -p <port>
//      user@127.0.0.1` shows a frozen LOCAL block containing the banner and
//      the password prompt, ending when the remote session begins (not when
//      ssh exits) — the rewritten line visibly carries every typed option;
//   2. command blocks on the REMOTE host from its first prompt, labelled with
//      the remote context (the typed destination);
//   3. after `exit`, local blocks again and the editor back;
//   4. a SECOND connection over the same option-bearing line is integrated
//      from its first prompt. Which DELIVERY FORM it picks is not asserted
//      here, and cannot be: the remote command is one unconditional bounded
//      loader for every connection (2026-08-20 carrier design §4.1), so
//      there is no second form to choose. The installed fact selects
//      nothing — the per-identity lookup that once fed the choice is
//      deleted (nocx-m8jwn.10) — it is only the inventory the footprint
//      surface lists;
//   5. authentication failure leaves an ordinary terminal: no passport, the
//      block runs to the local D and shows the real exit status;
//   6. the host's rc files are byte-identical after all of it (.bashrc,
//      .bash_profile, .profile, .zshrc, ${ZDOTDIR}/.zshrc) — N4, checked
//      against a real login;
//   7. ~/.nocx on the host holds exactly one active generation whose manifest
//      verifies (every file exists with the recorded hash and mode).
//
// One serial test: the second connection and the post-run checks MUST observe
// state the first connection installed, not an independent fixture.

// The disposable home the backend was launched with (headless path exports
// it; the wails-dev path uses the config's fixed .e2e/home). The REMOTE host
// is a separate home: the local app's ~/.nocx holds settings and staged
// launchers, while the fixture's ~/.nocx is where the launcher publishes the
// generation. Sharing them would mix two machines' state into one directory.
// The stand's home, asked of the stand. Same reason as shell-mode.spec.ts:
// NOCX_E2E_HOME_DIR lives in the backend's environment, not in this process's.
// Resolved when a test asks, never at module load.
//
// `readStand()` reads a manifest the stand writes in globalSetup, so a module
// that calls it while being IMPORTED demands a running backend just to be read.
// `playwright test --list` imports every spec and starts nothing, which is
// exactly what e2e/check-coverage.mjs runs — so a top-level call here took down
// the receipt that proves no spec file is uncollected, and would have taken the
// frontend job with it (nocx-z9s9.8).
const localHome = () => readStand().home
const e2eRoot = () => path.dirname(localHome())
const remoteHome = () => path.join(e2eRoot(), 'remote-home')
const remoteZdot = () => path.join(remoteHome(), 'zdot')

const RC_FILES = ['.bashrc', '.bash_profile', '.profile', '.zshrc', 'zdot/.zshrc']

// ── fixtures ──────────────────────────────────────────────────────────────

interface Fixture {
  proc: ChildProcess
  addr: string
  userKey: string
  knownHosts: string
  /** Resolves once the fixture has announced <n> client connections (CONN=). */
  waitConn(n: number, timeoutMs?: number): Promise<void>
}

/** Build (once per run) and spawn the in-process sshd; read its handshake. */
function startSshd(args: string[], env: NodeJS.ProcessEnv): Fixture & { _wait: Promise<void> } {
  const bin = path.resolve(
    process.env.TMPDIR ?? '/tmp',
    `nocx-e2e-sshd-${process.pid}-${Date.now()}`,
  )
  if (!existsSync(bin)) {
    execFileSync('go', ['build', '-o', bin, './cmd/e2e-sshd'], {
      cwd: path.resolve(__dirname, '..'),
    })
  }
  const proc = spawn(bin, args, { stdio: ['ignore', 'pipe', 'inherit'], env })
  const lines: string[] = []
  let addr = ''
  let userKey = ''
  let knownHosts = ''
  let ready = false
  let conns = 0
  let exited = false
  let stdoutRemainder = ''
  const connWaiters: Array<{
    n: number
    resolve: () => void
    reject: (e: Error) => void
    timer: NodeJS.Timeout
  }> = []

  // settleConns resolves every waiter whose connection count is now met.
  const settleConns = () => {
    for (let i = connWaiters.length - 1; i >= 0; i--) {
      if (conns >= connWaiters[i].n) {
        clearTimeout(connWaiters[i].timer)
        connWaiters[i].resolve()
        connWaiters.splice(i, 1)
      }
    }
  }

  const deadline = Date.now() + 15_000
  const reader = new Promise<void>((resolve, reject) => {
    proc.stdout?.on('data', (chunk: Buffer) => {
      // A line can split across pipe chunks; hold the partial tail until
      // the newline arrives so CONN=/READY detection never misses.
      stdoutRemainder += chunk.toString()
      const parts = stdoutRemainder.split('\n')
      stdoutRemainder = parts.pop() ?? ''
      for (const line of parts) {
        const trimmed = line.trim()
        if (!trimmed) continue
        lines.push(trimmed)
        if (trimmed.startsWith('ADDR=')) addr = trimmed.slice(5)
        if (trimmed.startsWith('USERKEY=')) userKey = trimmed.slice(8)
        if (trimmed.startsWith('KNOWNHOSTS=')) knownHosts = trimmed.slice(11)
        if (trimmed.startsWith('CONN=')) {
          conns++
          settleConns()
        }
        if (trimmed === 'READY') {
          ready = true
          resolve()
        }
      }
      if (!ready && Date.now() > deadline)
        reject(new Error(`e2e-sshd did not print READY: ${lines.join('|')}`))
    })
    proc.on('exit', (code) => {
      exited = true
      for (const w of connWaiters.splice(0)) {
        clearTimeout(w.timer)
        w.reject(new Error(`e2e-sshd exited (${code}) before ${w.n} connections`))
      }
      if (!ready) reject(new Error(`e2e-sshd exited early (${code}): ${lines.join('|')}`))
    })
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
    waitConn: (n: number, timeoutMs = 30_000) => {
      if (conns >= n) return Promise.resolve()
      if (exited) return Promise.reject(new Error(`e2e-sshd exited before ${n} connections`))
      return new Promise<void>((resolve, reject) => {
        const timer = setTimeout(() => {
          const i = connWaiters.findIndex((w) => w.resolve === resolve)
          if (i >= 0) connWaiters.splice(i, 1)
          reject(
            new Error(
              `e2e-sshd: saw ${conns}/${n} CONN= lines in ${timeoutMs}ms: ${lines.join('|')}`,
            ),
          )
        }, timeoutMs)
        connWaiters.push({ n, resolve, reject, timer })
      })
    },
    _wait: reader,
  } as Fixture & { _wait: Promise<void> }
}

/** The fixture's HOME is the remote host's home: separate from the backend's. */
function remoteEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    HOME: remoteHome(),
    ZDOTDIR: remoteZdot(),
    SSH_AUTH_SOCK: '', // a stray agent key must never shortcut the password path
    XDG_CONFIG_HOME: path.join(e2eRoot(), 'remote-config'),
    XDG_DATA_HOME: path.join(e2eRoot(), 'remote-data'),
    XDG_CACHE_HOME: path.join(e2eRoot(), 'remote-cache'),
  }
}

/** Seed the remote host's rc files (the N4 byte-identical snapshot source). */
function seedRemoteHome(): Record<string, Buffer> {
  mkdirSync(remoteZdot(), { recursive: true, mode: 0o700 })
  const content: Record<string, string> = {
    '.bashrc': `# nocx e2e remote .bashrc (P11 N4 fixture)\nexport E2E_RC_FINGERPRINT=1\n`,
    '.bash_profile': `# nocx e2e remote .bash_profile (P11 N4 fixture)\n`,
    '.profile': `# nocx e2e remote .profile (P11 N4 fixture)\n`,
    '.zshrc': `# nocx e2e remote .zshrc (P11 N4 fixture)\n`,
    'zdot/.zshrc': `# nocx e2e remote $ZDOTDIR/.zshrc (P11 N4 fixture)\n`,
  }
  const snapshot: Record<string, Buffer> = {}
  for (const name of RC_FILES) {
    const p = path.join(remoteHome(), name)
    writeFileSync(p, content[name], { mode: 0o600 })
    snapshot[name] = readFileSync(p)
  }
  return snapshot
}

/** The line the journey TYPES for a fixture — everything explicit, no alias,
 *  no config file. On this box OpenSSH resolves ~/.ssh/config from the passwd
 *  home, never from the disposable $HOME (nocx-mlm7 P11 finding), so an alias
 *  can never work here; the option-bearing form is what a real user may type,
 *  and it is exactly the option-carrying path nocx-c5az exists for. The
 *  fixture's key is explicit (-i), the host key is accepted without a
 *  known_hosts file (-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null),
 *  the port is explicit (-p), and NumberOfPasswordPrompts=1 makes one wrong
 *  password exit deterministically (the old config's alias carried it).
 *  planSsh must accept this line: every option is in VALUE_LETTERS. */
function typedSshLine(f: Fixture): string {
  return (
    `ssh -i ${f.userKey}` +
    ` -o StrictHostKeyChecking=no` +
    ` -o UserKnownHostsFile=/dev/null` +
    ` -o NumberOfPasswordPrompts=1` +
    ` -p ${f.addr.split(':')[1]} e2e@127.0.0.1`
  )
}

// ── the backend's installed-fact document (design §5.4) ───────────────────

/** One recorded installation, as the backend persisted it. */
interface InstalledFactRecord {
  identity: string
  protocol: string
  scriptVersion: string
  generation: string
}

function installedFactDoc(): { facts: Record<string, InstalledFactRecord> } | null {
  // documentDir, not a hand-spelled path. This looked in <root>/config/nocx-dev,
  // which was where XDG_CONFIG_HOME used to point; the stand gives the backend a
  // HOME and nothing else, so the documents are where the backend puts them —
  // Library/Application Support on darwin, .config elsewhere. A spec that spells
  // the path itself is right until the day it is not, and then it reports a
  // missing document rather than a wrong lookup (nocx-z9s9.3 was the same
  // shape).
  const p = path.join(documentDir(readStand().home), 'installed-facts.json')
  if (existsSync(p)) {
    try {
      return JSON.parse(readFileSync(p, 'utf8')) as { facts: Record<string, InstalledFactRecord> }
    } catch {
      return null
    }
  }
  return null
}

// ── the manifest verifier, mirroring Go's Publisher.Verify (publisher.go):
// a matching version string alone never proves an installation; every file
// the manifest names must exist with the recorded hash and mode ───────────

interface ManifestFile {
  hash: string
  mode: string
  size: number
}

function verifyInstallation(root: string): {
  generation: string
  version: string
  files: Record<string, ManifestFile>
} {
  // Mirror Go's Publisher.Verify exactly: Lstat everywhere — a symlink is a
  // refusal, not a file (design §4.1: no path in ~/.nocx is followed through
  // a symlink).
  const rootSt = lstatSync(root)
  expect(rootSt.isDirectory() && !rootSt.isSymbolicLink(), 'root is a real directory').toBe(true)
  const manifestPath = path.join(root, 'manifest.json')
  expect(existsSync(manifestPath), 'manifest.json exists').toBe(true)
  expect(lstatSync(manifestPath).isSymbolicLink(), 'manifest.json is not a symlink').toBe(false)
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as {
    protocol: number
    version: string
    generation: string
    files: Record<string, ManifestFile>
  }
  expect(manifest.protocol).toBe(1)
  expect(manifest.generation).toMatch(/^v[A-Za-z0-9._-]+$/)
  const genDir = path.join(root, 'integration', manifest.generation)
  const genSt = lstatSync(genDir)
  expect(genSt.isDirectory() && !genSt.isSymbolicLink(), 'generation dir is a real directory').toBe(
    true,
  )
  const names = Object.keys(manifest.files)
  expect(names.sort()).toEqual(['nocx.bash', 'nocx.posix', 'nocx.zsh'])
  for (const [name, mf] of Object.entries(manifest.files)) {
    const p = path.join(genDir, name)
    expect(existsSync(p), `${name} exists`).toBe(true)
    const st = lstatSync(p)
    expect(st.isFile() && !st.isSymbolicLink(), `${name} is a regular file`).toBe(true)
    expect(st.size, `${name} size`).toBe(mf.size)
    expect(st.mode & 0o777, `${name} mode`).toBe(parseInt(mf.mode, 8))
    const sha = crypto.createHash('sha256').update(readFileSync(p)).digest('hex')
    expect(`sha256:${sha}`, `${name} hash`).toBe(mf.hash)
  }
  return manifest as { generation: string; version: string; files: Record<string, ManifestFile> }
}

// ── helpers ───────────────────────────────────────────────────────────────

/** Submit a line through the editor (visible prompt) — shell-mode.spec's path. */
async function submitInEditor(page: Page, text: string): Promise<void> {
  const editor = page.locator('.pane.active .nocx-editor-input')
  await expect(editor).toBeVisible({ timeout: 10_000 })
  await editor.click()
  await editor.pressSequentially(text)
  await editor.press('Enter')
}

const pane = (page: Page) => page.locator('.pane.active')

// ── the journey ───────────────────────────────────────────────────────────

test('a hand-typed ssh: frozen local block, remote blocks, integrated second connection, auth failure, rc files intact, one generation', async ({
  page,
}) => {
  test.setTimeout(360_000)
  const nonce = Date.now().toString(36)
  const primaryBanner = `Welcome to the nocx e2e host ${nonce}`
  // The banner string MUST end with a newline: OpenSSH prints the banner
  // as-is and then a bare CR before the password prompt — without the
  // newline the prompt overwrites the banner's first 26 columns in the
  // buffer and the frozen block shows only the tail (verified against
  // OpenSSH 10.4 on this box, 2026-08-05).
  const failBanner = `Welcome to the auth-failure host ${nonce}`
  const primaryPassword = 'correct-horse-battery-staple'
  const wrongPassword = 'definitely-the-wrong-password'

  const primary = startSshd(
    ['-banner', `${primaryBanner}\n`, '-password', primaryPassword],
    remoteEnv(),
  )
  const failHost = startSshd(
    ['-banner', `${failBanner}\n`, '-password', 'the-other-password'],
    remoteEnv(),
  )
  try {
    await primary._wait
    await failHost._wait
    expect(primary.addr).not.toBe('')
    expect(failHost.addr).not.toBe('')

    // Seed BEFORE anything connects: the remote rc snapshot is the N4
    // baseline. No local ssh config or known_hosts: the typed line carries
    // everything (an alias cannot resolve on this box — see typedSshLine).
    const rcBefore = seedRemoteHome()
    // The wire observer that stood here watched the launcher CHOICE — which
    // delivery form the planner picked — and both its consumers are gone
    // with the surface they watched (nocx-292k). This journey now proves the
    // part a user can see, which is what it should have proved all along.

    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    const editor = page.locator('.pane.active .nocx-editor-input')
    await expect(editor).toBeVisible({ timeout: 20_000 })

    // The first A marker can land after the editor is visible (predecessor
    // finding, nocx-mlm7): a warmup command proves the local shell
    // integration is live before the ssh line, so the rewrite is planned
    // and the line is not sent raw.
    await submitInEditor(page, 'echo nocx-journey-warmup')
    const warmup = pane(page).locator('.cmd-block', { hasText: 'nocx-journey-warmup' })
    await expect(warmup).toBeVisible({ timeout: 30_000 })
    await expect(pane(page).locator('.cmd-block.cmd-block-running')).toHaveCount(0, {
      timeout: 30_000,
    })

    // ── 1. first connection: bootstrap; the ssh block is LOCAL and RUNNING
    // while the password prompt is up (no passport yet). A RUNNING block's
    // DOM text is only the recorded line — live output is painted to the
    // xterm canvas and serialized into the block only at freeze (nocx-q3y9
    // finding) — so while it runs, assert only what IS observable there:
    // the recorded line with every typed option.
    const primaryLine = typedSshLine(primary)
    await submitInEditor(page, primaryLine)
    const sshBlock = pane(page).locator('.cmd-block.cmd-block-running', { hasText: 'ssh -i' })
    await expect(sshBlock).toBeVisible({ timeout: 30_000 })
    await expect(pane(page).locator('.cmd-block.cmd-block-running')).toHaveCount(1, {
      timeout: 10_000,
    })
    // The options reached the plan (nocx-c5az): the recorded line carries
    // -i, both -o settings and -p verbatim.
    await expect(sshBlock).toContainText(`-i ${primary.userKey}`, { timeout: 10_000 })
    await expect(sshBlock).toContainText('-o StrictHostKeyChecking=no', { timeout: 10_000 })
    await expect(sshBlock).toContainText('-o UserKnownHostsFile=/dev/null', { timeout: 10_000 })
    await expect(sshBlock).toContainText('-o NumberOfPasswordPrompts=1', { timeout: 10_000 })
    await expect(sshBlock).toContainText(`-p ${primary.addr.split(':')[1]}`, { timeout: 10_000 })
    await expect(sshBlock).toContainText('e2e@127.0.0.1')
    // No passport yet: nothing has entered an environment.
    await expect(pane(page).locator('.cmd-block.cmd-block-entered')).toHaveCount(0)
    // The delivery-FORM assertions that stood here are gone with the surface
    // they watched (nocx-292k): shell.launcherCommand was deleted with the
    // P7 stream-and-passport path ADR-0024 forbids, and `.nocx/run/` was its
    // staging directory. What this journey still proves is the part a user
    // can see — the blocks, the banner, the prompt and the exits below.
    // Deterministic prompt readiness: the fixture prints CONN= when the
    // client's first userauth attempt reaches it (KEX done, one response
    // before the password prompt). The password is typed only after that —
    // a timed wait would race the prompt.
    await primary.waitConn(1, 30_000)
    // Nothing was staged on disk: the run directory is empty, or was never
    // created at all. Both mean the same thing and the second is what
    // actually happens — ADR-0022 made the ssh command line the carrier, so
    // no launcher is written anywhere (the comment above says as much about
    // `.nocx/run/` being the removed path's staging directory).
    //
    // Written as a bare readdirSync, it threw ENOENT instead of asserting,
    // and a spec cannot report on a directory by crashing on its absence.
    // Its own sibling at step 7 already guards existence this way; this is
    // the same check spelled the same way (nocx-c6z0 found it, having got
    // past the failure that used to hide it).
    const runDir = path.join(localHome(), '.nocx', 'run')
    if (existsSync(runDir)) {
      expect(readdirSync(runDir), 'staged bootstrap launcher consumed').toEqual([])
    }

    // The password goes to the pty, not the editor: the command owns input.
    await page.keyboard.type(primaryPassword)
    await page.keyboard.press('Enter')

    // Entry at `expected passport → tagged A → B`: the block FREEZES with no
    // exit code, labelled with the LOCAL cwd and no remote location. The
    // freeze serializes the buffer — the banner and the password prompt are
    // block text now. (The rewrite itself is never echoed into the buffer,
    // so the launcher path lives only in the sent frames and the consumed
    // run dir asserted above.)
    const entered = pane(page).locator('.cmd-block.cmd-block-entered')
    await expect(entered).toHaveCount(1, { timeout: 30_000 })
    await expect(entered).toContainText(primaryBanner)
    await expect(entered).toContainText('password:')
    await expect(entered.locator('.cmd-header-exit')).toHaveCount(0)
    await expect(entered.locator('.cmd-header-location')).toHaveCount(0)
    await expect(entered.locator('.cmd-header-cwd')).toHaveCount(1)
    await expect(pane(page).locator('.cmd-block.cmd-block-running')).toHaveCount(0, {
      timeout: 10_000,
    })

    // ── 2. command blocks on the REMOTE host from its first prompt ──
    // Through the editor, like every other command this test submits: the
    // remote prompt owns input here, and submitInEditor waits for the editor
    // to be up before typing. Raw page.keyboard.type does not wait, so it
    // raced the prompt's return — see the note at the second `exit` below.
    await submitInEditor(page, 'echo journey-1-ok')
    const remote1 = pane(page).locator('.cmd-block', { hasText: 'journey-1-ok' })
    await expect(remote1).toBeVisible({ timeout: 30_000 })
    // The remote context is the TYPED destination (the env label), not an
    // alias: `e2e@127.0.0.1`.
    await expect(remote1.locator('.cmd-header-location')).toHaveText('e2e@127.0.0.1', {
      timeout: 10_000,
    })
    await expect(remote1.locator('.cmd-header-cwd')).toHaveCount(0)

    // ── 3. exit: the remote session ends, local blocks again, editor back ──
    await submitInEditor(page, 'exit')
    // The exit block (remote context) freezes with NO code — the local D owns
    // the ssh command's status.
    //
    // Located by the COMMAND the user typed, not by the ssh client's
    // "Connection … closed." farewell. That text is printed by the LOCAL ssh
    // client after the far shell has already died, while the block freezes on
    // the far domain ending — two events on opposite sides of the connection
    // with nothing ordering them. Whether the farewell lands inside the block
    // or after it is therefore undetermined, and asserting it asserted which
    // way the race went: the journey passed when run alone and failed in the
    // full suite, on both engines, for as long as that locator stood
    // (nocx-8tf6).
    //
    // `.last()` because `exit` is the only command typed so far whose text
    // could match, and the most recent match is the block just submitted.
    const exitBlock = pane(page).locator('.cmd-block').filter({ hasText: 'exit' }).last()
    await expect(exitBlock).toBeVisible({ timeout: 30_000 })
    await expect(exitBlock.locator('.cmd-header-exit')).toHaveCount(0)
    await expect(editor).toBeVisible({ timeout: 20_000 })
    await expect(editor).toBeFocused({ timeout: 10_000 })

    // A local command runs locally again: local cwd chip, no location chip.
    await submitInEditor(page, 'echo local-after-exit')
    const local = pane(page).locator('.cmd-block', { hasText: 'local-after-exit' })
    await expect(local).toBeVisible({ timeout: 30_000 })
    await expect(local.locator('.cmd-header-cwd')).toHaveCount(1)
    await expect(local.locator('.cmd-header-location')).toHaveCount(0)

    // The installed fact was recorded from the first run: the far shell
    // named its generation on the authenticated channel and the backend
    // persisted it (nocx-ak2d).
    const factDoc = installedFactDoc()
    expect(factDoc, 'installed-facts.json exists').not.toBeNull()
    const facts = Object.values(factDoc!.facts)
    expect(facts.length).toBeGreaterThan(0)
    const fact = facts.find((f) => f.identity.includes(`127.0.0.1:${primary.addr.split(':')[1]}`))
    expect(fact, `installed fact for ${primary.addr}`).toBeTruthy()
    expect(fact!.protocol).toBe('1')

    // ── 4. second connection: integrated from its first prompt ──
    await submitInEditor(page, primaryLine)
    const secondRunning = pane(page).locator('.cmd-block.cmd-block-running', {
      hasText: 'ssh -i',
    })
    await expect(secondRunning).toBeVisible({ timeout: 30_000 })
    await expect(secondRunning).toContainText(`-p ${primary.addr.split(':')[1]}`)
    // There is no compact-second-connection assertion because there is no
    // compact second connection: every connection emits the same
    // unconditional loader (carrier design §4.1), and the installed fact
    // that once selected between forms no longer has a per-identity read at
    // all (nocx-m8jwn.10). Do not restore this assertion on the strength of
    // the fact having a writer again — the writer feeds the footprint
    // inventory, not a delivery choice.
    await primary.waitConn(2, 30_000)
    await page.keyboard.type(primaryPassword)
    await page.keyboard.press('Enter')
    // The second ssh entered: the LAST entered block carries the banner and
    // the prompt, whichever ordinal it happens to be.
    //
    // This counted entered blocks and expected exactly three — ssh1, the
    // step-3 remote `exit`, ssh2 — on the reading that a cut-short remote
    // exit freezes with the same no-code entered paint. It does not, and
    // more importantly it is not required to: nocx-mlyu established that
    // whether `exit` becomes an attempt at all is A RACE THE PRODUCT CANNOT
    // WIN, because the command usually destroys the shell that would have
    // sent the start frame. The renderer therefore has two legitimate
    // answers — bound and frozen, or unbound and abandoned as `unknown` —
    // and both are pinned in frontend/src/lifecycle/projections.test.ts.
    // Counting the class asserted which side of that race came up.
    //
    // `.last()` is what makes this deterministic rather than merely looser:
    // the exit block precedes ssh2 chronologically either way, so the most
    // recent entered block is ssh2 whether the count is two or three.
    const enteredBlocks = pane(page).locator('.cmd-block.cmd-block-entered')
    const secondSsh = enteredBlocks.last()
    await expect(secondSsh).toContainText(primaryBanner, { timeout: 30_000 })
    await expect(secondSsh).toContainText('password:')
    await submitInEditor(page, 'echo journey-2-ok')
    await expect(pane(page).locator('.cmd-block', { hasText: 'journey-2-ok' })).toBeVisible({
      timeout: 30_000,
    })
    // The block being visible means the OUTPUT arrived, not that the prompt is
    // back and owns input again — the editor is still hidden for a moment
    // after it. Typing raw into that gap sent `exit` nowhere, and the Enter
    // then landed on the editor the instant it appeared, empty: that empty
    // submit used to throw with the editor already hidden and strand it for
    // the rest of the session (nocx-axqs). submitInEditor waits for the thing
    // it needs — the editor — instead of betting on the machine.
    await submitInEditor(page, 'exit')
    await expect(editor).toBeVisible({ timeout: 20_000 })

    // ── 5. authentication failure: ordinary terminal, real exit status ──
    const enteredBeforeFail = await pane(page).locator('.cmd-block.cmd-block-entered').count()
    await submitInEditor(page, typedSshLine(failHost))
    const failRunning = pane(page).locator('.cmd-block.cmd-block-running', {
      hasText: 'ssh -i',
    })
    await expect(failRunning).toBeVisible({ timeout: 30_000 })
    await failHost.waitConn(1, 30_000)
    await page.keyboard.type(wrongPassword)
    await page.keyboard.press('Enter')
    // No passport: no environment entry, the block runs to the local D and
    // carries the REAL exit status of the ssh process.
    const failBlock = pane(page).locator('.cmd-block', { hasText: failBanner })
    await expect(failBlock.locator('.cmd-header-exit-fail')).toHaveText('exit 255', {
      timeout: 30_000,
    })
    await expect(failBlock).toContainText('Permission denied')
    // The fail run entered nothing: the entered-block set is unchanged. (The
    // count is 4 by now — the cut-short remote `exit` blocks also freeze
    // with the entered paint, the only no-code presentation — so a relative
    // count is the meaningful assertion, not an absolute one.)
    await expect(pane(page).locator('.cmd-block.cmd-block-entered')).toHaveCount(enteredBeforeFail)
    await expect(editor).toBeVisible({ timeout: 20_000 })

    // ── 6. the host's rc files are byte-identical after all of it ──
    for (const name of RC_FILES) {
      const p = path.join(remoteHome(), name)
      expect(existsSync(p), `${name} still exists`).toBe(true)
      expect(readFileSync(p), name).toEqual(rcBefore[name])
    }

    // ── 7. ~/.nocx on the host: one active generation, manifest verifies ──
    const root = path.join(remoteHome(), '.nocx')
    const manifest = verifyInstallation(root)
    const genDirs = readdirSync(path.join(root, 'integration')).filter((n) => n.startsWith('v'))
    // What this journey is checking is that a HAND-TYPED ssh published no
    // generation of its own beside the managed one. It is not "the host holds
    // exactly one directory": the publisher's documented policy is keep-two —
    // the active generation and the newest other survive, so a shell still
    // running from the previous version does not have its directory deleted
    // out from under it (publisher.go, cleanupOrphans).
    //
    // Asserting exactly one passed only because CI checks out a fresh tree and
    // .e2e/remote-home starts empty, so no version bump had ever been seen
    // here. The first one — nocx-tyyo, 40 -> 41 — failed it on a local run
    // while the product did precisely what it promises. The assertion is
    // therefore the invariant the product actually holds, and it still fails
    // if this run mints a second generation, which is the thing being tested.
    expect(genDirs).toContain(manifest.generation)
    expect(genDirs.length).toBeLessThanOrEqual(2)
    for (const g of genDirs) {
      if (g === manifest.generation) continue
      expect(
        Number(g.slice(1)),
        `${g} is a leftover from an older version, not a second generation minted by this run`,
      ).toBeLessThan(Number(manifest.generation.slice(1)))
    }
    expect(statSync(path.join(root, 'launch')).mode & 0o777).toBe(0o700)
    expect(statSync(path.join(root, 'manifest.json')).mode & 0o777).toBe(0o600)
    expect(statSync(root).mode & 0o777).toBe(0o700)
    expect(readdirSync(path.join(root, 'tmp'))).toEqual([])
    expect(existsSync(path.join(root, 'lock'))).toBe(false)
    // The backend recorded the SAME generation the host's manifest names —
    // re-read at the END, because the fact is upserted by every accepted
    // passport: the second connection's launch-carrier passport names the
    // committed generation, while the argv launcher's skip-path passport
    // renders "-" when the generation was already installed (launcher_publish.go:
    // NOCX_GENERATION is exported only from a publish, and the scripts
    // default the field to "-").
    const finalFactDoc = installedFactDoc()
    const finalFact =
      finalFactDoc &&
      Object.values(finalFactDoc.facts).find((f) =>
        f.identity.includes(`127.0.0.1:${primary.addr.split(':')[1]}`),
      )
    expect(finalFact, 'fact re-read at step 7').toBeTruthy()
    expect(finalFact!.generation).toBe(manifest.generation)

    // The staged argv launchers were consumed exactly once (nocx-sxdd): the
    // local run directory is still empty after both connections (runDir is
    // declared at step 1, where the first consumption is asserted).
    if (existsSync(runDir)) {
      expect(readdirSync(runDir)).toEqual([])
    }
  } finally {
    primary.proc.kill('SIGKILL')
    failHost.proc.kill('SIGKILL')
  }
})
