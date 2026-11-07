/**
 * e2e: a person's call waiting past two seconds takes a row in Operations,
 * with its catalogue sentence and Cancel. Ordinary work that answers quickly
 * leaves the list empty (ADR-8eej3e66 §8).
 *
 * THE ROW KEEPS ONE OF THREE ANSWERS WHEN THE CALL IS A MUTATION, because the
 * question does not go when the row does (nocx-3an1s.2.11). `connections.test`
 * is a call the catalogue says may have reached a host by its deadline, so
 * cancelling it ends the WAIT and opens the question: the row stops offering
 * Cancel and says "did not start", "done" or "checking what happened". It is
 * never a retry. The spec used to assert the row disappeared, which was the
 * wait leaving rather than the answer being known — the guarantee it stood for
 * is that an answer is always visible, and that is what it now asserts.
 *
 * The slow call is a real connections.test through the real backend: a local
 * listener accepts the SSH connection and never answers the handshake, so the
 * probe waits on the wire exactly as it would on a host that stopped
 * responding. It is reachable because the spec and the backend share the
 * container's network namespace.
 */
import { createServer, type Server, type Socket } from 'node:net'
import { generateKeyPairSync } from 'node:crypto'
import { chmodSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
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
  ).toBeVisible({ timeout: 20_000 })
}

test.use({ viewport: { width: 1280, height: 800 } })

test('a waiting call takes a row, and Cancel takes it away with the answer', async ({ page }) => {
  const host = await silentSSHHost()
  const keyDir = mkdtempSync(join(tmpdir(), 'nocx-operations-key-'))
  const keyPath = join(keyDir, 'id_rsa')
  const { privateKey } = generateKeyPairSync('rsa', {
    modulusLength: 2048,
    privateKeyEncoding: { type: 'pkcs1', format: 'pem' },
    publicKeyEncoding: { type: 'spki', format: 'pem' },
  })
  writeFileSync(keyPath, privateKey)
  chmodSync(keyPath, 0o600)
  const profileName = `${PROFILE_PREFIX}${Date.now()}`
  let profileId: string | null = null
  try {
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })
    await appReadyForInput(page)
    const endpoint = await resolveBackend(page)
    await bindEndpoint(page, endpoint)
    const wire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      const created = (await wire.call('profiles.create', {
        type: 'ssh',
        name: profileName,
        options: { host: '127.0.0.1', port: host.port, user: 'e2e', keyPath },
      })) as { id?: string }
      profileId = created.id ?? null
    } finally {
      wire.close()
    }
    expect(profileId).not.toBeNull()

    await openConnections(page)
    // Ordinary work before the slow call answered inside the threshold.
    await page.locator('button[data-view="operations"]').click()
    await expect(page.getByTestId('operations-panel')).toContainText('Nothing is running')

    await page.getByRole('button', { name: `Test connection to ${profileName}` }).click()

    // Past two seconds: one row with the catalogue's sentence and Cancel.
    const row = page.locator('.ui-operation-row').filter({ hasText: 'Checking the connection' })
    await expect(row).toBeVisible({ timeout: 6_000 })
    const cancel = page.getByRole('button', { name: 'Cancel Checking the connection' })
    await expect(cancel).toBeVisible()
    await cancel.click()

    // A ROW IS THE WAIT AND GOES WHEN THE WAIT DOES (ADR-8eej3e66 §8), on
    // every settlement: the row is gone, nothing is left in Operations, and
    // there is nothing left to cancel.
    await expect(row).toHaveCount(0, { timeout: 30_000 })
    await expect(page.getByTestId('operations-panel')).toContainText('Nothing is running')
    await expect(page.locator('.ui-operation-row')).toHaveCount(0)

    // WHERE THE ANSWER GOES. `connections.test` is a mutation (the catalogue
    // declares its outcome unknown at the deadline), so §8 owes the person
    // one of three sentences — and the call's own rejection carries it to
    // the surface that asked: the Connections panel's test toast. A row that
    // stayed behind to read it out was the entry a Cancel could not remove.
    await expect(page.locator('.ui-toast__message')).toHaveText(
      /It did not start|It finished|Checking what happened/,
      { timeout: 30_000 },
    )
  } finally {
    for (const socket of host.held) socket.destroy()
    await new Promise<void>((resolve) => host.server.close(() => resolve()))
    rmSync(keyDir, { recursive: true, force: true })
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
