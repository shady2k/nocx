/**
 * e2e: a person's call waiting past two seconds is a row in Operations, with
 * its catalogue sentence and Cancel; the row goes when the call settles and
 * leaves no finished entry. Ordinary work that answers quickly leaves the
 * list empty (ADR-8eej3e66 §8).
 *
 * The slow call is a real one: a local TCP listener accepts the SSH connection
 * and never completes the handshake, so `connections.test` waits on the wire
 * exactly as it would on a host that stopped answering.
 */
import { createServer, type Server, type Socket } from 'node:net'
import {
  appReadyForInput,
  bindEndpoint,
  openControlPlane,
  resolveBackend,
  settingsReady,
  test,
  expect,
  type Page,
} from './harness'

const PROFILE_PREFIX = 'e2e-operations-waiting-'

async function silentSSHHost(): Promise<{ server: Server; port: number; held: Socket[] }> {
  const held: Socket[] = []
  const server = createServer((socket) => {
    held.push(socket)
    socket.on('error', () => {})
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  if (address === null || typeof address === 'string') throw new Error('no listener address')
  return { server, port: address.port, held }
}

async function openConnections(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
  await expect(
    page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }),
  ).toBeVisible({ timeout: 10_000 })
}

test.use({ viewport: { width: 1280, height: 800 } })

test('a waiting call takes a row, cancels, and leaves the list empty', async ({ page }) => {
  const host = await silentSSHHost()
  const profileName = `${PROFILE_PREFIX}${Date.now()}`
  let profileId: string | null = null
  try {
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })
    await appReadyForInput(page)
    const endpoint = await resolveBackend(page)
    await bindEndpoint(page, endpoint)
    const wire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      const created = (await wire.call('profiles.create', {
        type: 'ssh',
        name: profileName,
        options: { host: '127.0.0.1', port: host.port, user: 'e2e' },
      })) as { id?: string }
      profileId = created.id ?? null
    } finally {
      wire.close()
    }
    expect(profileId).not.toBeNull()

    // Ordinary work first: open the settings tab and the connections page.
    // Those calls answer inside the threshold, so nothing may take a row.
    await openConnections(page)
    await expect(page.locator('button[data-view="operations"]')).toBeVisible()
    await page.locator('button[data-view="operations"]').click()
    await expect(page.getByTestId('operations-panel')).toContainText('Nothing is running')

    // The slow call. It is a person's Test action through the real client.
    await page.getByRole('button', { name: `Test connection to ${profileName}` }).click()

    // Under two seconds: no row. The action's own pending state is the only
    // sign, which the disabled "Testing..." label carries.
    await expect(page.getByRole('button', { name: 'Testing...' })).toBeVisible()

    // Past two seconds: one row, the catalogue's sentence, and Cancel.
    const row = page.locator('.ui-operation-row').filter({ hasText: 'Checking the connection' })
    await expect(row).toBeVisible({ timeout: 10_000 })
    const cancel = page.getByRole('button', { name: 'Cancel Checking the connection' })
    await expect(cancel).toBeVisible()

    await cancel.click()
    await expect(row).toHaveCount(0)
    await expect(page.getByTestId('operations-panel')).toContainText('Nothing is running')
    // No finished entry: the list is empty, not a cancelled row.
    await expect(page.locator('.ui-operation-row')).toHaveCount(0)
  } finally {
    for (const socket of host.held) socket.destroy()
    await new Promise<void>((resolve) => host.server.close(() => resolve()))
    if (profileId !== null) {
      const endpoint = await resolveBackend(page).catch(() => null)
      if (endpoint !== null) {
        const wire = await openControlPlane(endpoint.port, endpoint.token).catch(() => null)
        if (wire !== null) {
          await wire.call('profiles.delete', { id: profileId }).catch(() => {})
          wire.close()
        }
      }
    }
  }
})
