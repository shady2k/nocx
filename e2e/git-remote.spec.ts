// The remote-helper epic's acceptance test (plan Task 12): the check that
// decides whether a user can commit from the panel on a machine that is not
// theirs.
//
// DONE WHEN, verbatim:
//
//   On an SSH tab in a remote repository containing a file whose name has a
//   space, a quote, a leading `-` and a newline: stage exactly that row
//   through the panel, then commit a multi-line message with quotes and
//   non-ASCII text, against a remote pre-commit hook that writes a marker
//   file and emits more than one packet of output. Assert the marker exists
//   ON THE REMOTE HOST, exactly that one path was staged, the exact message
//   is HEAD's, and the returned status is fresh and complete.
//
// Every clause is load-bearing, and each is asserted at the seam that makes
// it honest:
//
//   - The hostile filename is the argv test (D3/D8). The design forbids argv
//     and sends pathspecs over stdin; the byte-exact `git diff --cached
//     --name-only -z` assertion through a real SSH exec proves the path
//     crossed the wire whole, and the panel row is staged by clicking its own
//     stage button — never by constructing the path in the test.
//   - The multi-line non-ASCII message with quotes is the stdin test (D8,
//     `git commit -F -`). `git log -1 --format=%B` on the remote host is
//     compared byte for byte with what was typed.
//   - The marker proves the commit ran on the REMOTE machine with that
//     repository's own hooks — the epic's entire promise. It is checked
//     through an SSH exec (a local existsSync would prove nothing), and it
//     is asserted ABSENT before the commit, so a stale marker can never
//     satisfy the later assertion.
//   - The hook emits more than one packet of output (44 KiB to each of
//     stdout and stderr — larger than one SSH data packet, under the commit
//     capture bound), so output that stopped at a packet boundary or a
//     full pipe would surface as a broken commit.
//   - "Fresh and complete" is the read path: both lists empty and the count
//     zero right after the commit, applied from the commit response itself —
//     a stale pre-commit status would still show the staged row.
//
// The fixture (cmd/e2e-sshd) is the remote host: it serves the shell over a
// real pty, exec over pipes, and the sftp subsystem the helper install rides
// on, and it seeds the repository (hostile file, hook, marker path) at
// startup. The spec drives the REAL panel over the REAL transport — nothing
// is stubbed but the Wails bindings, exactly as git-panel.spec.ts does.
//
// Timing: every wait is on an observable state change — a row appearing, a
// badge appearing, a marker file existing. There is no waitForTimeout in
// this file.
import { appReadyForInput, test, expect, resolveBackend } from './harness'
import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path, { join } from 'node:path'
import type { ChildProcess } from 'node:child_process'
import type { Page } from './harness'

// ── Selectors (read from frontend/src/git/git-panel.tsx — not invented) ──

const VIEW_GIT = 'button[data-view="git"]'
const PANEL = '[data-testid="git-panel"]'
const BRANCH = '[data-testid="git-branch"]'
const COUNT = '[data-testid="git-changed-count"]'
const STAGED = '[data-testid="git-staged-list"]'
const UNSTAGED = '[data-testid="git-unstaged-list"]'
const COMMIT = '[data-testid="git-commit"]'
const SUBJECT = '#git-commit-subject'
const BODY = '#git-commit-body'
const CONSENT = '[data-testid="git-consent-required"]'
const ACCEPT = '[data-testid="git-consent-accept"]'
const ROW = '.ui-collection-row'
const TAB = '.nocx-tab'

/** The disposable home the backend was launched with (headless path exports
 *  it; the wails-dev path uses the config's fixed .e2e/home) — where the
 *  backend's ssh client reads known_hosts from. */
const LOCAL_HOME = process.env.NOCX_E2E_HOME_DIR || path.resolve(__dirname, '..', '.e2e', 'home')

/** The multi-line message with quotes and non-ASCII (D8, stdin half). Typed
 *  into the panel; asserted byte for byte as HEAD's message.
 *
 *  git stores the message with one trailing newline, and `git log
 *  --format=%B` adds its own — the same +2 the service unit test pins
 *  (hostsvc: "git appends one trailing newline to stored messages; log -B
 *  adds its own"). */
const MESSAGE_SUBJECT = `add the "quoted" file — it's hostile ✓`
const MESSAGE_BODY = `A body line with 'quotes' and "escapes".\nNon-ASCII: naïve résumé ✓`
const MESSAGE = `${MESSAGE_SUBJECT}\n\n${MESSAGE_BODY}\n\n`

interface RemoteFixture {
  proc: ChildProcess
  addr: string
  userKey: string
  knownHosts: string
  /** The seeded repository's absolute path on the remote host. */
  repo: string
  /** The absolute path the remote pre-commit hook writes its marker to. */
  marker: string
  /** The hostile file's name, repo-relative: a space, a quote, a leading
   *  dash and a newline. */
  hostile: string
  _wait: Promise<void>
}

/** Build (once per run) and spawn the fixture with a seeded repository; read
 *  its handshake. The repo is created by the fixture AT seedRoot — the spec
 *  owns the directory for cleanup, the fixture owns what goes in it. */
function startRemoteSshd(seedRoot: string): RemoteFixture {
  const bin = path.resolve(
    process.env.TMPDIR ?? '/tmp',
    `nocx-e2e-sshd-${process.pid}-${Date.now()}`,
  )
  if (!existsSync(bin)) {
    execFileSync('go', ['build', '-o', bin, './cmd/e2e-sshd'], {
      cwd: path.resolve(__dirname, '..'),
    })
  }
  const proc = spawn(bin, ['-repo', seedRoot], { stdio: ['ignore', 'pipe', 'inherit'] })
  const lines: string[] = []
  let addr = ''
  let userKey = ''
  let knownHosts = ''
  let repo = ''
  let marker = ''
  let hostile = ''
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
        if (trimmed.startsWith('REPO=')) repo = trimmed.slice(5)
        if (trimmed.startsWith('MARKER=')) marker = trimmed.slice(7)
        if (trimmed.startsWith('HOSTILE_B64=')) {
          hostile = Buffer.from(trimmed.slice(12), 'base64').toString('utf8')
        }
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
    get repo() {
      return repo
    },
    get marker() {
      return marker
    },
    get hostile() {
      return hostile
    },
    _wait: reader,
  }
}

/** Seed the isolated home's known_hosts so the backend's ssh client accepts
 *  the fixture's host key. REPLACED, not appended: every fixture spawn mints
 *  fresh keys, and a stale line for a dead key makes the backend refuse. */
function trustHostKey(fixture: RemoteFixture): void {
  const sshDir = path.join(LOCAL_HOME, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
}

/** One bounded command ON THE REMOTE HOST, through the fixture's own SSH
 *  server. This is the seam every remote-side assertion uses: the marker,
 *  the staged set and HEAD's message are facts of the machine that owns the
 *  repository, and a local existsSync or a local git invocation would prove
 *  none of them. */
function sshExec(fixture: RemoteFixture, command: string): string {
  return execFileSync(
    'ssh',
    [
      '-i',
      fixture.userKey,
      '-o',
      'StrictHostKeyChecking=no',
      '-o',
      'UserKnownHostsFile=/dev/null',
      '-o',
      'NumberOfPasswordPrompts=1',
      '-p',
      fixture.addr.split(':')[1],
      'e2e@127.0.0.1',
      command,
    ],
    { encoding: 'utf8' },
  )
}

/** Whether a file exists on the remote host, via `test -f` over SSH. */
function remoteFileExists(fixture: RemoteFixture, p: string): boolean {
  try {
    sshExec(fixture, `test -f '${p}'`)
    return true
  } catch {
    return false
  }
}

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

test('a commit from the panel, on a remote host, through its own pre-commit hook', async ({
  page,
}) => {
  test.setTimeout(180_000)
  // The spec owns the seed root for cleanup; the fixture seeds the repo at
  // it. Under the isolated home when the headless path declares one, else
  // the system tmp dir — the same choice git-fixture.ts makes.
  const seedRoot = mkdtempSync(join(process.env.NOCX_E2E_HOME_DIR ?? tmpdir(), 'nocx-remote-'))
  const fixture = startRemoteSshd(seedRoot)
  let createdProfileId: string | null = null
  let wsPort: number | null = null
  let wsToken: string | null = null
  try {
    await fixture._wait
    expect(fixture.repo).not.toBe('')
    expect(fixture.hostile).not.toBe('')
    trustHostKey(fixture)

    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    await appReadyForInput(page)

    // Read the backend port/token through the bindings (stubbed on the
    // headless path, real under wails dev) — the same seam shell-mode uses.
    const wsInfo = await resolveBackend(page)

    // Seed the connection the way Settings would. The name is unique per
    // run: the nocx-server store persists across runs in this home.
    const profileName = `e2e-git-remote-${Date.now()}`
    wsPort = wsInfo.port
    wsToken = wsInfo.token
    const created = await rpc<{ id?: string }>(page, wsInfo.port, wsInfo.token, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.addr.split(':')[0],
        port: Number(fixture.addr.split(':')[1]),
        user: 'e2e',
        keyPath: fixture.userKey,
        // No shellIntegration option: the default destination mode (script)
        // wraps and installs the launcher automatically, which is what
        // makes the remote shell emit OSC 7 so the cwd — and with it the
        // tab title and git.open's origin — lands. 'ask' would leave the
        // session conventional and the panel would never see a cwd.
      },
    })
    createdProfileId = created?.id ?? null

    // Open the connection through quick connect: the palette's host search
    // reaches a saved profile and Enter opens it directly.
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    const option = page.locator('.quick-connect__item', { hasText: profileName })
    await expect(option).toBeVisible({ timeout: 10_000 })
    await page.keyboard.press('Enter')

    // The SSH tab opens and becomes active. The remote shell starts INSIDE
    // the seeded repository (the fixture chdirs there) and the launcher
    // rcfile dials the lifecycle channel, so the first prompt's OSC 7
    // carries the repository path and the frontend records it VERIFIED.
    //
    // The tab title is deliberately NOT the cwd oracle: the lane seed sets
    // programTitle to the ssh host (domain-environment.ts:235 — "the tab
    // names the destination"), and pushTitle prefers programTitle over the
    // cwd label. The panel is the right surface: the git store's rescope
    // refuses to ask git.open for a session whose cwd is not verified, so
    // the consent card appearing below is itself the proof the cwd landed.
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })

    // The git panel answers for THAT tab. The ask comes first: consent at
    // the feature (D8) — a fresh home has no grant for this host.
    await page.locator(VIEW_GIT).click()
    await expect(page.locator(PANEL)).toBeVisible({ timeout: 30_000 })
    await expect(page.locator(CONSENT)).toBeVisible({ timeout: 30_000 })
    await page.locator(ACCEPT).click()

    // Accept raises the machine to the relay tier: the helper installs over
    // the fixture's sftp subsystem, the dial answers, and git.open returns
    // ok. The branch badge is the store's own word for it.
    await expect(page.locator(BRANCH)).toBeVisible({ timeout: 60_000 })
    await expect(page.locator(BRANCH)).toHaveText('main')

    // The hostile file is the repository's ONLY change: exactly one
    // unstaged row. The name contains a newline, so the row is asserted by
    // count, never by text matching; the byte-exact identity is the SSH
    // assertion below.
    const unstagedRow = page.locator(UNSTAGED).locator(ROW)
    await expect(unstagedRow).toHaveCount(1, { timeout: 30_000 })
    await expect(page.locator(COUNT)).toHaveText('1 changed')

    // Stage exactly that row through the panel.
    await unstagedRow.getByTestId('git-row-stage').click()
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(1, { timeout: 30_000 })
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)

    // The remote index holds EXACTLY that one path, byte for byte — the D8
    // argv test over the wire. `git diff --cached --name-only -z` is NUL
    // terminated, so the expected value is the hostile name plus a NUL.
    const staged = sshExec(fixture, `git -C '${fixture.repo}' diff --cached --name-only -z`)
    expect(staged).toBe(`${fixture.hostile}\0`)

    // No marker yet: the hook has not run. Absent BEFORE the commit is what
    // makes the after-commit assertion able to go red — a marker satisfied
    // by a stale file from an earlier run would pass a test that proves
    // nothing.
    expect(remoteFileExists(fixture, fixture.marker)).toBe(false)

    // The multi-line message with quotes and non-ASCII (D8, stdin half).
    await page.locator(SUBJECT).fill(MESSAGE_SUBJECT)
    await page.locator(BODY).fill(MESSAGE_BODY)
    await page.locator(COMMIT).click()

    // The commit ran ON THE REMOTE HOST, through that repository's own
    // pre-commit hook: the marker exists there. The poll waits on the
    // observable state, never on a duration.
    await expect.poll(() => remoteFileExists(fixture, fixture.marker)).toBe(true)

    // HEAD's message is the exact message, byte for byte.
    const head = sshExec(fixture, `git -C '${fixture.repo}' log -1 --format=%B`)
    expect(head).toBe(MESSAGE)

    // The commit touched exactly that one path.
    const committed = sshExec(
      fixture,
      `git -C '${fixture.repo}' diff-tree --no-commit-id --name-only -r -z HEAD`,
    )
    expect(committed).toBe(`${fixture.hostile}\0`)

    // The returned status is fresh and complete: the commit response
    // carried the post-commit status (applied under the store's rule 1), so
    // both lists emptied and the count is zero without waiting for the next
    // poll. A stale pre-commit status would still show the staged row.
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(0, { timeout: 30_000 })
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)
    await expect(page.locator(COUNT)).toHaveText('0 changed')
    await expect(page.locator(BRANCH)).toHaveText('main')
  } finally {
    // Best-effort cleanup inside the existing finally: a cleanup that
    // throws would replace the real failure with its own.
    if (createdProfileId !== null && wsPort !== null && wsToken !== null) {
      await rpc(page, wsPort, wsToken, 'profiles.delete', { id: createdProfileId }).catch(
        () => undefined,
      )
    }
    fixture.proc.kill('SIGKILL')
    rmSync(seedRoot, { recursive: true, force: true })
  }
})
