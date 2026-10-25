/** nocx-l3evv: exercise the saved helper SSH path, not a local PTY. */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { BASE_URL } from './base-url'
import {
  standalone as test,
  expect,
  bindEndpoint,
  openControlPlane,
  promptReady,
  type BackendEndpoint,
  VaultBackend,
} from './harness'
import { startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

test('three full-screen SSH commands retain only their own output', async ({ browser }, info) => {
  test.setTimeout(600_000)
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'nocx-tui-remote-'))
  // The earlier listing is recognizable and cannot be mistaken for htop output.
  writeFileSync(path.join(remoteHome, 'BEFORE-THE-FULLSCREEN-COMMAND'), '')
  const backend = new VaultBackend(readStand().server, {
    root: mkdtempSync(path.join(tmpdir(), 'nocx-tui-backend-')),
  })
  let fixture: SshdFixture | null = null
  const observations: unknown[] = []
  const blocks: { entry: string | null; output: string }[] = []
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  // The real socket carries both the accepted lifecycle facts and screen frames.
  // Keep them in arrival order so a restored primary frame cannot masquerade as
  // the program's last alternate frame in the diagnostic evidence.
  page.on('websocket', (socket) =>
    socket.on('framereceived', (event) => {
      if (typeof event.payload === 'string') {
        const message = JSON.parse(event.payload) as { method?: string; params?: unknown }
        if (
          message.method?.startsWith('lifecycle.') ||
          message.method?.startsWith('block.') ||
          message.method === 'session.effect'
        ) {
          observations.push(message)
        }
      } else if (event.payload[1] === 2) {
        const frame = JSON.parse(event.payload.subarray(18).toString()) as {
          revision: number
          state: unknown
          rows: { text: string }[]
        }
        observations.push({
          revision: frame.revision,
          state: frame.state,
          rows: frame.rows.map((row) => row.text),
        })
      }
    }),
  )
  try {
    fixture = await startSshd({ home: remoteHome, cwd: remoteHome })
    const ep = await backend.start()
    const sshDir = path.join(backend.isolatedHome, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
    await ask(ep, 'profiles.create', {
      type: 'ssh',
      name: 'Full-screen regression',
      options: {
        host: fixture.host,
        port: fixture.port,
        user: 'e2e',
        keyPath: fixture.userKey,
        desiredMode: 'helper',
      },
    })
    await bindEndpoint(page, ep)
    await page.goto('/')
    await promptReady(page)
    const beforeSession = await page.locator('.pane.active').getAttribute('data-session-id')
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill('Full-screen regression')
    await expect(
      page.locator('.quick-connect__item', { hasText: 'Full-screen regression' }),
    ).toBeVisible()
    await page.keyboard.press('Enter')
    await expect(page.locator('.pane.active')).not.toHaveAttribute(
      'data-session-id',
      beforeSession!,
    )
    const editor = page.locator('.pane.active .nocx-editor-input')
    await expect(editor).toBeVisible({ timeout: 120_000 })
    await editor.fill('ls')
    await editor.press('Enter')
    await expect(page.locator('.pane.active .cmd-block .cmd-output')).toContainText(
      'BEFORE-THE-FULLSCREEN-COMMAND',
      { timeout: 30_000 },
    )
    await expect(editor).toBeVisible()

    for (let run = 0; run < 3; run++) {
      observations.push({ run: run + 1, event: 'submit htop' })
      await editor.fill('htop')
      await editor.press('Enter')
      const running = page.locator('.pane.active .cmd-block-running')
      await expect(running).toHaveCount(1)
      const entry = await running.getAttribute('data-entry-id')
      const live = page.locator('.pane.active .terminal-live-grid')
      await expect(live).toContainText('Tasks:', { timeout: 30_000 })
      await page.keyboard.press('q')
      await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, {
        timeout: 30_000,
      })
      await expect(editor).toBeVisible()
      const frozen = page.locator(`.pane.active .cmd-block[data-entry-id="${entry}"]`)
      blocks.push({ entry, output: (await frozen.locator('.cmd-output').textContent()) ?? '' })
    }
    for (const block of blocks) {
      expect(block.output).not.toContain('BEFORE-THE-FULLSCREEN-COMMAND')
      expect(block.output).toContain('Tasks:')
    }
  } catch (error) {
    const visibleBlocks = await page.locator('.pane.active .cmd-block').evaluateAll((elements) =>
      elements.map((element) => ({
        entry: element.getAttribute('data-entry-id'),
        running: element.classList.contains('cmd-block-running'),
        command: element.querySelector('.cmd-header-text')?.textContent,
        output: element.querySelector('.cmd-output')?.textContent,
      })),
    )
    console.error(
      'FULLSCREEN_BOUNDARY_EVIDENCE',
      JSON.stringify({ observations, blocks, visibleBlocks }),
    )
    throw error
  } finally {
    await info.attach('screen-and-boundary-order', {
      body: JSON.stringify({ observations, blocks }, null, 2),
      contentType: 'application/json',
    })
    await context.close()
    fixture?.proc.kill('SIGTERM')
    backend.stop()
  }
})
