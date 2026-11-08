/**
 * e2e: a tab opened from a saved connection says the connection's name, with
 * its host beside it — in the tab and in the composer's place line
 * (nocx-ouru5.1.2).
 *
 * The owner, 2026-11-08: an ssh tab opened from a saved connection showed
 * '77.91.73.42' as its title and 'dev@<ip> : ~' in the composer. The name is
 * the one the person gave the connection, and it was on no surface at all.
 * Wanted: both, the name first and the host beside it; a connection with no
 * name keeps the host alone.
 *
 * Against a REAL sshd (cmd/e2e-sshd) and a REAL saved profile: the tab title
 * is composed from the connection the pane was opened from, so a fixture that
 * merely drew the strings would prove nothing about where they came from. The
 * two profiles are opened through the quick-connect palette, which is the
 * door a person uses — and the one that used to drop the connection's name.
 */
import { appReadyForInput, expect, test, resolveBackend } from './harness'
import type { Page } from './harness'
import { readStand } from './stand'
import { startSshd, type SshdFixture } from './sshd-fixture'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const TAB_TITLE = '.nocx-tab-title'
const COMPOSER_CONTEXT = '.pane.active .nocx-editor-context .ui-prompt-context'

/** The two connections this spec saves: one named, one not. Their users
 *  differ so the palette's search reaches exactly one row each — a profile
 *  row's label is `user@host`, and two rows on one host would be one search
 *  away from each other. */
const NAMED = { name: 'Production Web', id: 'ssh:e2e-connection-name', user: 'websrv' }
const UNNAMED = { name: '', id: 'ssh:e2e-connection-unnamed', user: 'barebox' }

/** The isolated home the backend runs with — where its known_hosts lives. */
const backendHome = (): string => readStand().home

/** Trust the fixture's host key up front, so the open never stops at the
 *  consent dialog: this spec is about what the tab is CALLED. */
function trustHostKey(fixture: SshdFixture): void {
  const dir = path.join(backendHome(), '.ssh')
  mkdirSync(dir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(dir, 'known_hosts'), fixture.knownHosts + '\n')
}

interface Endpoint {
  port: number
  token: string
}

/** The fixture fields a page can carry — plain strings and numbers only: a
 *  `ChildProcess` handed to page.evaluate is a serialisation error, not a
 *  profile. */
interface FixtureAddress {
  host: string
  port: number
  userKey: string
}

/** Save one connection the way Settings would (shell-mode.spec.ts's own seam:
 *  one JSON-RPC call over the real socket). */
async function saveProfile(
  page: Page,
  endpoint: Endpoint,
  profile: { id: string; name: string; user: string },
  fixture: FixtureAddress,
): Promise<void> {
  await page.evaluate(
    ({ port, token, profile, fixture }) =>
      new Promise<void>((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, [`nocx.token.${token}`])
        const timer = setTimeout(() => reject(new Error('profiles.create timed out')), 10_000)
        ws.onopen = () =>
          ws.send(
            JSON.stringify({
              jsonrpc: '2.0',
              id: 1,
              method: 'profiles.create',
              params: {
                id: profile.id,
                type: 'ssh',
                name: profile.name,
                options: {
                  host: fixture.host,
                  port: fixture.port,
                  user: profile.user,
                  keyPath: fixture.userKey,
                  desiredMode: 'script',
                },
              },
            }),
          )
        ws.onmessage = (ev: MessageEvent) => {
          const msg = JSON.parse(String(ev.data)) as { error?: { message?: string } }
          clearTimeout(timer)
          ws.close()
          if (msg.error) reject(new Error(`profiles.create: ${msg.error.message ?? 'rpc error'}`))
          else resolve()
        }
        ws.onerror = () => {
          clearTimeout(timer)
          reject(new Error('profiles.create: websocket error'))
        }
      }),
    { port: endpoint.port, token: endpoint.token, profile, fixture },
  )
}

async function deleteProfile(page: Page, endpoint: Endpoint, id: string): Promise<void> {
  await page.evaluate(
    ({ port, token, id }) =>
      new Promise<void>((resolve) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, [`nocx.token.${token}`])
        ws.onopen = () =>
          ws.send(
            JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'profiles.delete', params: { id } }),
          )
        ws.onmessage = () => {
          ws.close()
          resolve()
        }
        ws.onerror = () => resolve()
      }),
    { port: endpoint.port, token: endpoint.token, id },
  )
}

/** Open a saved connection through the palette, the door a person uses. */
async function openFromPalette(page: Page, search: string, rowMatches: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const field = page.locator('.quick-connect__search input')
  await expect(field).toBeVisible()
  await field.fill(search)
  // The saved row must be the thing Enter picks (shell-mode.spec.ts's own
  // guard: an empty list would fall back to the ad-hoc host row).
  await expect(page.locator('.quick-connect__item', { hasText: rowMatches })).toBeVisible({
    timeout: 10_000,
  })
  await page.keyboard.press('Enter')
  // The editor coming up is the remote shell reaching a prompt.
  await expect(page.locator('.pane.active .nocx-editor-input')).toBeVisible({ timeout: 30_000 })
}

test('a saved connection names its tab and its composer place line', async ({ page }) => {
  test.setTimeout(120_000)
  const remoteHome = path.join(path.dirname(backendHome()), `conn-name-remote-${process.pid}`)
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  const fixture = await startSshd({ home: remoteHome, cwd: remoteHome })
  const saved: string[] = []
  let endpoint: Endpoint | null = null
  try {
    trustHostKey(fixture)
    await page.goto('/')
    await appReadyForInput(page)
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    endpoint = await resolveBackend(page)

    const address: FixtureAddress = {
      host: fixture.host,
      port: fixture.port,
      userKey: fixture.userKey,
    }
    await saveProfile(page, endpoint, NAMED, address)
    saved.push(NAMED.id)
    await saveProfile(page, endpoint, UNNAMED, address)
    saved.push(UNNAMED.id)

    // ── THE NAMED CONNECTION ───────────────────────────────────────────
    await openFromPalette(page, NAMED.name, NAMED.name)
    const titles = page.locator(TAB_TITLE)
    // The name first and the host beside it — the connection's own words,
    // not the address alone the owner was shown.
    await expect(titles.last()).toHaveText(`${NAMED.name} · ${fixture.host}`, { timeout: 10_000 })
    const context = page.locator(COMPOSER_CONTEXT)
    await expect(context.locator('[data-part="name"]')).toHaveText(NAMED.name)
    await expect(context.locator('[data-part="host"]')).toHaveText(`${NAMED.user}@${fixture.host}`)
    // Name first, host beside it: the order a person reads.
    await expect(context).toContainText(`${NAMED.name}·${NAMED.user}@${fixture.host}`)

    // ── A CONNECTION WITH NO NAME KEEPS THE HOST ALONE ─────────────────
    await openFromPalette(page, `${UNNAMED.user}@${fixture.host}`, UNNAMED.user)
    await expect(titles.last()).toHaveText(fixture.host, { timeout: 10_000 })
    const bare = page.locator(COMPOSER_CONTEXT)
    await expect(bare.locator('[data-part="host"]')).toHaveText(`${UNNAMED.user}@${fixture.host}`)
    await expect(bare.locator('[data-part="name"]')).toHaveCount(0)
  } finally {
    for (const id of saved) {
      if (endpoint) await deleteProfile(page, endpoint, id).catch(() => undefined)
    }
    fixture.proc.kill('SIGKILL')
  }
})
