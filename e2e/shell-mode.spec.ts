import { appReadyForInput, test, expect, resolveBackend } from './harness'
import { readStand } from './stand'
import { spawn, execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, existsSync } from 'node:fs'
import path from 'node:path'
import type { ChildProcess } from 'node:child_process'
import type { Page } from './harness'

// ── Shell-mode happy path (nocx-4t37.2) ───────────────────────────────────
// The epic's acceptance: a user who has never read the docs lands on a plain
// SSH shell, SEES the capability statement, switches it to nocxify, and gets
// blocks — one automated check, end to end, against a REAL shell on a REAL
// PTY. The fixture (cmd/e2e-sshd) is an in-process SSH server that actually
// executes commands, so the in-band bootstrap runs for real and the OSC 133
// markers are the shell's own.

// The home the BACKEND resolved, from the stand that started it — not a guess
// at it. NOCX_E2E_HOME_DIR is set in the BACKEND's environment, not in
// Playwright's, so reading it here answered undefined and fell back to a path
// that is only sometimes right. A known_hosts written to the wrong home is a
// host key the backend never sees (nocx-z9s9.6).
const e2eHome = () => readStand().home

interface Fixture {
  proc: ChildProcess
  addr: string
  userKey: string
  knownHosts: string
}

/** Build (once per run) and spawn the in-process sshd; read its handshake. */
function startSshd(): Fixture {
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
  // — the answer nocxify-journey.spec.ts already gives its own sshd.
  //
  // Without it the premise of this spec cannot be staged. The local backend
  // installs ~/.nocx and the shell rc hooks into its HOME on every start, and
  // an sshd sharing that HOME spawns a shell that sources them: the connection
  // comes up ALREADY integrated, the chip offers "Enable command editor", and
  // "a plain SSH shell" is a state this machine cannot be in. A real remote
  // host has its own home; so does the fixture (nocx-z9s9.8).
  const remoteHome = path.join(path.dirname(e2eHome()), 'shell-mode-remote-home')
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
  // The fixture prints ADDR/USERKEY/KNOWNHOSTS/READY then serves forever.
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
 *  the fixture's host key (the nocx-server runs with that HOME). The file is
 *  REPLACED, not appended: every fixture spawn mints fresh keys, and a stale
 *  line for a dead key makes the backend refuse the connection. */
function trustHostKey(fixture: Fixture): void {
  const sshDir = path.join(e2eHome(), '.ssh')
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

test('an SSH connection comes up integrated and its commands become blocks', async ({ page }) => {
  test.setTimeout(90_000)
  const fixture = startSshd()
  let createdId: string | null = null
  try {
    await (fixture as Fixture & { _wait: Promise<void> })._wait
    expect(fixture.addr).not.toBe('')
    trustHostKey(fixture)

    await page.goto('/')
    await appReadyForInput(page)
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    // Read the backend port/token through the bindings (stubbed on the
    // headless path, real under wails dev) — the same seam auth.spec uses.
    const wsInfo = await resolveBackend(page)

    // Seed the connection the way Settings would: a profile pointing at the
    // fixture, on the default destination mode (script). The name is unique per
    // run: the nocx-server store persists across runs in this home, and a
    // stale profile from an earlier run would dial a dead fixture.
    const profileName = `e2e-fixture-${Date.now()}`
    const created = await rpc<{ id: string }>(page, wsInfo.port, wsInfo.token, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.addr.split(':')[0],
        port: Number(fixture.addr.split(':')[1]),
        user: 'e2e',
        keyPath: fixture.userKey,
      },
    })
    createdId = created.id

    // Open the connection through quick connect: the palette's host search
    // reaches a saved profile and Enter opens it DIRECTLY (no vault
    // preflight — the profile's key is file-based), which is the user path
    // the epic's happy path describes.
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    // Wait for the saved row before Enter. The palette renders its search box
    // before its providers have answered, and on an EMPTY list Enter used to
    // reach the ad-hoc "Connect to <host>" fallback — dialling root@<the profile
    // name> with no credential, which is what CI caught (nocx-k1691). The row is
    // the observable state that says the saved profile is the thing Enter picks.
    await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
      timeout: 10_000,
    })
    await page.keyboard.press('Enter')

    // The SSH tab opens INTEGRATED, and the editor chrome offers nothing —
    // which is the healthy state (capability.ts: integrated + editor yields no
    // action at all).
    //
    // This is a deliberate change to what the spec proves, and the reason is a
    // product change it never caught up with. The epic was written when a plain
    // shell was where a user landed: the chip would read "Integrate this shell"
    // and clicking it was the switch. Since `script` became the default
    // destination mode (nocx-mlm7, contracts/open.schema.json — "script (the
    // default — N3) wraps and installs automatically"), a fresh connection to a
    // bash host arrives already integrated, so there is nothing to switch.
    //
    // The two states either side of it are both real and neither produces the
    // old assertion: in `raw` the product deliberately offers nothing
    // (terminal-content.ts:2211, authorized = policy !== 'raw'), and in
    // `script` integration succeeds. "Plain shell WITH the offer" now needs
    // script mode plus a shell the launcher refuses — a shellIntegrationReason
    // case, and its own test.
    //
    // What survives here is the end of the epic's journey, which is the part
    // that matters to a user: a real remote shell on a real PTY produces
    // blocks. The switch path is not covered by anything now, and that is
    // written down rather than papered over (nocx-z9s9.10).
    // The editor coming UP is what says the remote shell reached a prompt, so
    // it is waited for first and it carries the whole budget. The order is not
    // cosmetic: the recovery chip lives INSIDE the editor's root, and a hidden
    // prompt is `display: none` on that root (editor.ts hide()), so "recovery
    // is not visible" is satisfied by the exact state the next line rejects.
    // Asserted before the editor is up it closes vacuously, spends none of its
    // 20s on the thing being waited for, and leaves the editor 10s of a
    // 30s wait — which is how this passed under chromium and timed out under
    // webkit on the same run. Same shape as nocx-sx4sg: a wait whose predicate
    // the failure state also satisfies.
    const editor = page.locator('.pane.active .nocx-editor-input')
    await expect(editor).toBeVisible({ timeout: 30_000 })

    // And now the healthy state means something: the prompt is up and its
    // chrome offers no recovery.
    const recovery = page.locator('.pane.active .nocx-editor-recovery')
    await expect(recovery).not.toBeVisible()

    // The user then runs a command through the nocx editor, and it becomes
    // a block — the epic's "switches it to nocxify, and gets blocks".
    await editor.click()
    await editor.pressSequentially('echo hello-from-e2e')
    await editor.press('Enter')
    const block = page.locator('.pane.active .cmd-block', {
      hasText: 'hello-from-e2e',
    })
    await expect(block.first()).toBeVisible({ timeout: 30_000 })
  } finally {
    // Take the profile back out. The stand's home is shared by every spec in
    // the run AND by both browser projects, so a profile left here becomes the
    // next spec's starting state — quick-connect's picker asserts the plain
    // server list is EMPTY, and went red on this one across the project
    // boundary, where nothing in either file points at the other (nocx-8rda).
    try {
      const info = await resolveBackend(page)
      if (createdId) await rpc(page, info.port, info.token, 'profiles.delete', { id: createdId })
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})
