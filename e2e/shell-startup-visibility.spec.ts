import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

import { appReadyForInput, test, expect, openControlPlane, resolveBackend } from './harness'
import { readStand } from './stand'

const CHECKING = 'Checking this shell…'
const COMMAND = 'sleep 20'

interface Fixture {
  proc: ChildProcess
  addr: string
  userKey: string
  knownHosts: string
  ready: Promise<void>
}

function startSshd(remoteHome: string): Fixture {
  const bin = join(tmpdir(), `nocx-startup-sshd-${process.pid}-${Date.now()}`)
  execFileSync('go', ['build', '-o', bin, './cmd/e2e-sshd'], { cwd: join(__dirname, '..') })
  const proc = spawn(bin, [], {
    stdio: ['ignore', 'pipe', 'inherit'],
    env: { ...process.env, HOME: remoteHome, ZDOTDIR: remoteHome },
  })
  let addr = ''
  let userKey = ''
  let knownHosts = ''
  const lines: string[] = []
  const ready = new Promise<void>((resolve, reject) => {
    proc.stdout?.on('data', (chunk: Buffer) => {
      for (const line of chunk.toString().split('\n')) {
        const value = line.trim()
        if (!value) continue
        lines.push(value)
        if (value.startsWith('ADDR=')) addr = value.slice(5)
        if (value.startsWith('USERKEY=')) userKey = value.slice(8)
        if (value.startsWith('KNOWNHOSTS=')) knownHosts = value.slice(11)
        if (value === 'READY') resolve()
      }
    })
    proc.on('exit', (code) => reject(new Error(`e2e-sshd exited (${code}): ${lines.join('|')}`)))
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
    ready,
  }
}

// This is a user's first action in a new remote pane. Submit through the real
// composer while startup is still being checked; waiting for readiness first
// would skip the defect this test guards against.
test('a command submitted during shell startup has a visible running block', async ({ page }) => {
  const remoteHome = mkdtempSync(join(tmpdir(), 'nocx-shell-startup-remote-'))
  const startupRelease = join(remoteHome, '.startup-release')
  writeFileSync(
    join(remoteHome, '.bashrc'),
    'while [ ! -e "$HOME/.startup-release" ]; do sleep 0.05; done\n',
  )
  const fixture = startSshd(remoteHome)
  let profileId: string | null = null
  const profileName = `startup-visibility-${Date.now()}`
  try {
    await fixture.ready
    expect(fixture.addr).not.toBe('')

    await page.goto('/')
    await appReadyForInput(page)
    const backend = await resolveBackend(page)
    const sshDir = join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

    const wire = await openControlPlane(backend.port, backend.token)
    try {
      const profile = (await wire.call('profiles.create', {
        type: 'ssh',
        name: profileName,
        options: {
          host: fixture.addr.split(':')[0],
          port: Number(fixture.addr.split(':')[1]),
          user: 'e2e',
          keyPath: fixture.userKey,
          desiredMode: 'script',
        },
      })) as { id: string }
      profileId = profile.id
    } finally {
      wire.close()
    }

    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(page.locator('.quick-connect__item').first()).toContainText('Local shell')
    await search.fill(profileName)
    await expect(page.locator('.quick-connect__item').first()).toContainText(profileName)
    await page.keyboard.press('Enter')

    await expect(page.locator('#pane-2')).toBeVisible()
    const pane = page.locator('#pane-2')
    const checking = pane.getByRole('status', { name: CHECKING })
    const editor = pane.locator('.nocx-editor-input')
    await editor.focus()
    await editor.fill(COMMAND)
    await expect(editor).toHaveText(COMMAND)
    await expect(checking).toBeVisible({ timeout: 0 })
    await page.keyboard.press('Enter')
    writeFileSync(startupRelease, '')

    await expect(pane.locator('.cmd-header[data-variant="running"] .ui-spinner')).toBeVisible()
    await expect(pane.locator('.ui-process-bar')).toHaveCount(0)
    const block = pane.locator('.cmd-block.cmd-block-running').filter({ hasText: COMMAND })
    await expect(block).toBeVisible()
  } finally {
    if (profileId) {
      try {
        const backend = await resolveBackend(page)
        const wire = await openControlPlane(backend.port, backend.token)
        try {
          await wire.call('profiles.delete', { id: profileId })
        } finally {
          wire.close()
        }
      } catch {
        // Preserve the test's failure; cleanup is best-effort.
      }
    }
    fixture.proc.kill('SIGKILL')
    // Best effort, like every other cleanup here: the remote shell is a setsid
    // child of the sshd, not killed with it, and as the closed pty hangs it up
    // it can still be writing into this home — CI saw ENOTEMPTY replace a
    // passing result. A leftover temp dir is harmless; a cleanup that throws
    // reports a failure the test never had.
    try {
      rmSync(remoteHome, { recursive: true, force: true })
    } catch {
      // Left for the container's tmp to take.
    }
  }
})
