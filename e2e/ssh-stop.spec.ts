/**
 * A command on an ssh host ends as a finished block, and Stop ends it
 * (nocx-xn63t.7).
 *
 * The owner pressed a running block's Stop on an ssh pane and saw the
 * command go on running. Two defects stood behind it: the far helper's pane
 * never had its block output streamed to the coordinator, so NO block on an
 * ssh pane ever finished — a command that ended by itself stayed "Running" —
 * and session.signal answered every remote session "unsupported" before
 * asking the helper, which can signal the host's own foreground group.
 */
import { appReadyForInput, test, expect, promptReady, resolveBackend } from './harness'
import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path, { join } from 'node:path'
import type { ChildProcess } from 'node:child_process'
import { rpc } from './sshd-fixture'

const TAB = '.nocx-tab'
const LOCAL_HOME = process.env.NOCX_E2E_HOME_DIR || path.resolve(__dirname, '..', '.e2e', 'home')

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

// Both integration methods that give a pane blocks. Helper runs the shell
// under the far helper, which can name the host's foreground group; Script
// runs it under the local helper's ssh client, which cannot, and Stop there
// answered "unreconciled" without writing a byte (nocx-xn63t.7, reported on
// the owner's own host after the Helper case was green).
for (const method of ['Helper', 'Script'] as const) {
  test(`Stop ends a command running on an ssh host (${method})`, async ({ page }) => {
    test.setTimeout(180_000)
    const seedRoot = mkdtempSync(join(process.env.NOCX_E2E_HOME_DIR ?? tmpdir(), 'nocx-stop-'))
    const fixture = startRemoteSshd(seedRoot)
    try {
      await fixture._wait
      trustHostKey(fixture)
      await page.goto('/')
      await expect(page.locator(TAB)).toHaveCount(1)
      await appReadyForInput(page)
      const wsInfo = await resolveBackend(page)
      const profileName = `e2e-ssh-stop-${Date.now()}`
      await rpc(page, wsInfo, 'profiles.create', {
        type: 'ssh',
        name: profileName,
        options: {
          host: fixture.addr.split(':')[0],
          port: Number(fixture.addr.split(':')[1]),
          user: 'e2e',
          keyPath: fixture.userKey,
        },
      })
      await page.keyboard.press('Control+Shift+P')
      const search = page.locator('.quick-connect__search input')
      await expect(search).toBeVisible()
      await search.fill(profileName)
      await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
        timeout: 10_000,
      })
      await page.keyboard.press('Enter')
      const helperDialog = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
      await expect(helperDialog).toBeVisible({ timeout: 30_000 })
      await helperDialog.getByRole('radio', { name: method }).click()
      await helperDialog.getByRole('button', { name: 'Continue' }).click()
      await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })
      await promptReady(page)
      await expect(
        page.locator('.pane.active').getByRole('status', { name: 'Checking this shell…' }),
      ).toHaveCount(0, { timeout: 30_000 })

      // A command that ends by itself finishes as a block.
      const done = `DONE-${Date.now().toString(36)}`
      await page.keyboard.type(`echo ${done}; sleep 1`)
      await page.keyboard.press('Enter')
      await expect(page.locator('.pane.active .cmd-block', { hasText: done })).not.toHaveClass(
        /cmd-block-running/,
        { timeout: 30_000 },
      )
      await promptReady(page)

      // And a long one ends at Stop.
      const marker = `STOP-${Date.now().toString(36)}`
      await page.keyboard.type(`echo ${marker}; sleep 300`)
      await page.keyboard.press('Enter')
      const running = page.locator('.pane.active .cmd-block-running')
      await expect(running).toHaveCount(1, { timeout: 30_000 })
      await expect(page.locator('.pane.active .terminal-live-grid')).toContainText(marker, {
        timeout: 30_000,
      })

      await running.getByRole('button', { name: 'Stop' }).click()
      await expect(running).toHaveCount(0, { timeout: 15_000 })
      await promptReady(page)
    } finally {
      fixture.proc.kill()
    }
  })
}
