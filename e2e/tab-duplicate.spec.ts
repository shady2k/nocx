import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { appReadyForInput, promptReady, test, expect, resolveBackend } from './harness'
import { readStand } from './stand'
import { startSshd, type SshdFixture } from './sshd-fixture'

// nocx-ouru5.1.4: the tab's right-click menu offered Rename, Pin and Close, and
// one thing was missing — a copy.
//
// Duplicate opens a NEW tab immediately to the right of its source, in the same
// place: the source pane's own directory for a local tab, and the same
// connection for an ssh tab (a NEW session on that host, never a mirror of the
// old tab's screen). Both cases are driven the way a person reaches it — the
// tab's own context menu — and asserted where a person reads the answer: the
// strip's order, the new tab's title, and a command that runs in it.

const TAB = '.nocx-tab'
const MENU_ITEM = '.ui-context-menu__item'

async function duplicateTab(page: import('./harness').Page, index: number): Promise<void> {
  await page.locator(TAB).nth(index).click({ button: 'right' })
  await expect(page.locator(MENU_ITEM).first()).toBeVisible({ timeout: 10_000 })
  await page.locator(MENU_ITEM, { hasText: 'Duplicate' }).first().click()
  await expect(page.locator(MENU_ITEM)).toHaveCount(0, { timeout: 10_000 })
}

/** The strip's rows, in drawing order, as their pane ids. */
async function stripIds(page: import('./harness').Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll('.nocx-tab')).map(
      (t) => t.getAttribute('data-pane-id') ?? '',
    ),
  )
}

/** One JSON-RPC call over the real backend socket, as the app does. */
async function rpc<T>(page: import('./harness').Page, method: string, params: unknown): Promise<T> {
  const info = await resolveBackend(page)
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
    { port: info.port, token: info.token, method, params },
  )
}

test('duplicating a local tab opens it right after, in the same directory, and it runs a command', async ({
  page,
}) => {
  const work = path.join(readStand().home, 'duplicate-me')
  mkdirSync(work, { recursive: true })

  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1, { timeout: 15_000 })
  await promptReady(page)

  // A second tab, so "right after its source" is a place with a neighbour on
  // both sides rather than the end of the strip — and the FIRST one is the
  // source, put in front again by hand.
  await page.keyboard.press('Meta+t')
  await expect(page.locator(TAB)).toHaveCount(2, { timeout: 15_000 })
  await page.keyboard.press('Meta+1')
  await expect(page.locator(TAB).nth(0)).toHaveAttribute('aria-selected', 'true', {
    timeout: 10_000,
  })

  // Leave home: a copy that landed back in home would still be "a tab", and
  // the title is how a person sees the difference.
  await page.locator('.pane.active .nocx-editor-input').click()
  await page.keyboard.type(`cd '${work}'`)
  await page.keyboard.press('Enter')
  const title = page.locator(TAB).nth(0).locator('.nocx-tab-title')
  await expect(title).toHaveText('~/duplicate-me', { timeout: 15_000 })

  // Duplicate the FIRST tab: the copy is the SECOND row, not the last.
  const before = await stripIds(page)
  await duplicateTab(page, 0)
  await expect(page.locator(TAB)).toHaveCount(3, { timeout: 20_000 })
  const after = await stripIds(page)
  const fresh = after.filter((id) => !before.includes(id))
  expect(fresh, 'a duplicate adds exactly one row').toHaveLength(1)
  expect(after.indexOf(fresh[0]), 'the copy sits immediately right of its source').toBe(1)
  expect(after[0], 'the source did not move').toBe(before[0])

  // The copy is in the SAME DIRECTORY — its own shell says so, on its title and
  // in the output of a command it runs.
  await promptReady(page)
  const copyTitle = page.locator(TAB).nth(1).locator('.nocx-tab-title')
  await expect(copyTitle).toHaveText('~/duplicate-me', { timeout: 20_000 })
  await page.locator('.pane.active .nocx-editor-input').click()
  await page.keyboard.type('pwd')
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .cmd-block', { hasText: 'duplicate-me' }).last(),
  ).toBeVisible({ timeout: 20_000 })

  // …and it is a copy of the TAB, not of the screen: the source keeps its own
  // session.
  const sourceTitle = page.locator(TAB).nth(0).locator('.nocx-tab-title')
  await expect(sourceTitle).toHaveText('~/duplicate-me')
  const sessions = await page.evaluate(() =>
    Array.from(document.querySelectorAll('.pane')).map((p) => p.getAttribute('data-session-id')),
  )
  expect(new Set(sessions.filter((id) => id)).size).toBeGreaterThanOrEqual(2)
})

test.describe('an ssh tab duplicates onto the same connection, as a new session', () => {
  let fixture: SshdFixture | null = null
  let profileId: string | null = null

  test.afterAll(async () => {
    fixture?.proc.kill('SIGKILL')
  })

  test('duplicating an ssh tab opens a second tab on the same host that runs a command', async ({
    page,
  }) => {
    test.setTimeout(180_000)
    const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-duplicate-ssh-'))
    const remoteHome = path.join(hostRoot, 'home')
    mkdirSync(remoteHome, { recursive: true, mode: 0o700 })

    const host = await startSshd({ home: remoteHome, cwd: remoteHome })
    fixture = host
    try {
      // The far host's key, trusted in the home the backend runs under.
      // REPLACED, never appended: every fixture spawn mints a fresh key.
      const sshDir = path.join(readStand().home, '.ssh')
      mkdirSync(sshDir, { recursive: true, mode: 0o700 })
      writeFileSync(path.join(sshDir, 'known_hosts'), `${host.knownHosts}\n`)

      // The page first: the binding the rpc helper reaches the backend
      // through is installed as an init script, so it exists from the first
      // navigation onwards and not on about:blank.
      await page.goto('/')
      await expect(page.locator(TAB)).toHaveCount(1, { timeout: 15_000 })
      await appReadyForInput(page)

      // The connection a person would open: a saved host, with the
      // integration mode declared so the connect-time ask is not what this
      // spec measures.
      const profileName = `duplicate-host-${Date.now()}`
      const created = await rpc<{ id: string }>(page, 'profiles.create', {
        type: 'ssh',
        name: profileName,
        options: {
          host: host.host,
          port: host.port,
          user: host.user,
          keyPath: host.userKey,
          desiredMode: 'script',
        },
      })
      profileId = created.id

      // Open it through quick connect, the palette's own route.
      await page.keyboard.press('Control+Shift+P')
      const search = page.locator('.quick-connect__search input')
      await expect(search).toBeVisible({ timeout: 10_000 })
      await search.fill(profileName)
      await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
        timeout: 10_000,
      })
      await page.keyboard.press('Enter')
      await expect(page.locator(TAB)).toHaveCount(2, { timeout: 60_000 })
      await expect(page.locator('.pane.active .nocx-editor-input')).toBeVisible({ timeout: 60_000 })

      const sourceTab = page.locator(TAB).nth(1)
      await expect(sourceTab).toHaveAttribute('aria-selected', 'true', { timeout: 20_000 })
      const sourceSession = await page.locator('.pane.active').getAttribute('data-session-id')

      // THE COPY, from the tab's own menu.
      const before = await stripIds(page)
      await duplicateTab(page, 1)
      await expect(page.locator(TAB)).toHaveCount(3, { timeout: 60_000 })
      await expect
        .poll(
          async () => {
            const after = await stripIds(page)
            const fresh = after.filter((id) => !before.includes(id))
            return fresh.length === 1 ? after.indexOf(fresh[0]) : -2
          },
          { timeout: 60_000, message: 'the copy was not drawn right after its source' },
        )
        .toBe(2)

      // A NEW SESSION, not a mirror: the copy's pane is on a session of its
      // own, and it runs a command of its own.
      await expect(page.locator('.pane.active .nocx-editor-input')).toBeVisible({ timeout: 60_000 })
      const copySession = await page.locator('.pane.active').getAttribute('data-session-id')
      expect(copySession).not.toBeNull()
      expect(copySession, 'a duplicate opens a new session').not.toBe(sourceSession)
      await page.locator('.pane.active .nocx-editor-input').click()
      await page.keyboard.type('echo COPIED-CONNECTION-RUNS')
      await page.keyboard.press('Enter')
      await expect(
        page.locator('.pane.active .cmd-block', { hasText: 'COPIED-CONNECTION-RUNS' }),
      ).toBeVisible({ timeout: 60_000 })

      // AND THE SAME DESTINATION: the chain says both panes apply at one
      // endpoint over one kind — the backend's own answer, not a reading of
      // the screen.
      const layout = await rpc<{
        panes: { id: string; kind: string; endpoint: string | null; tabId: string }[]
        tabs: { id: string; workspaceId: string }[]
      }>(page, 'layout.read', {})
      const sshPanes = layout.panes.filter((p) => p.kind === 'ssh')
      expect(sshPanes.length, 'the source and its copy are the two ssh panes').toBe(2)
      expect(new Set(sshPanes.map((p) => p.endpoint)).size, 'one endpoint').toBe(1)
      expect(sshPanes[0].tabId, 'two tabs, two panes').not.toBe(sshPanes[1].tabId)
      expect(new Set(layout.tabs.map((t) => t.id)).size).toBe(3)
    } finally {
      // A PROFILE LEFT BEHIND IS THE NEXT SPEC'S STARTING STATE: it dials a
      // fixture that is about to be killed, and quick-connect's own picker
      // asserts an empty server list (nocx-8rda). Deleting it here, in a
      // finally, is what makes a FAILED run leave nothing either.
      try {
        if (profileId !== null) await rpc(page, 'profiles.delete', { id: profileId })
      } catch {
        // A cleanup that throws would replace the real failure with its own.
      }
      host.proc.kill('SIGKILL')
    }
  })
})
