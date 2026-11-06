/**
 * e2e: a mutation carries an identity, the backend retains what became of it,
 * and operation.outcome answers for it — so "did it happen?" has an answer and
 * a replay has none (ADR-8eej3e66 §4 and §8).
 *
 * Three things are proven against the real backend:
 *
 *  - a real mutation that completed answers `done`, and the same identity sent
 *    again is REFUSED with mutation-replayed before any handler runs;
 *  - a mutation that is still running answers `outcome-unknown` (the person is
 *    "checking what happened"), and stops doing so when the work stops;
 *  - an identity the backend never saw answers `unknown`, and names nothing.
 *
 * The last test drives the surface a person reaches: the Operations row for a
 * waiting call. Cancelling a call that may have changed something keeps the
 * row and turns it into one of §8's three answers — it never becomes a retry.
 *
 * The slow call is a real connections.test through the real backend: a local
 * listener accepts the SSH connection and never answers the handshake, so the
 * probe waits on the wire exactly as it would on a host that stopped
 * responding. That is the pattern operations-waiting-call.spec.ts established.
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
  type ControlPlane,
  type Page,
} from './harness'

const PROFILE_PREFIX = 'e2e-mutation-outcome-'

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

async function ready(page: Page): Promise<ControlPlane> {
  await page.goto('/')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
  const endpoint = await resolveBackend(page)
  await bindEndpoint(page, endpoint)
  return openControlPlane(endpoint.port, endpoint.token)
}

interface OutcomeAnswer {
  outcome: 'not-started' | 'done' | 'outcome-unknown' | 'unknown'
  operation: string
}

test.use({ viewport: { width: 1280, height: 800 } })

test('a completed mutation answers done, and the same identity is never run twice', async ({
  page,
}) => {
  const wire = await ready(page)
  const name = `${PROFILE_PREFIX}${Date.now()}`
  const identity = `e2e-mutation-done-${Date.now()}`
  let profileId: string | null = null
  try {
    const created = await wire.callWithMutation(
      'profiles.create',
      { type: 'ssh', name, options: { host: '127.0.0.1', port: 1, user: 'e2e' } },
      identity,
    )
    expect(created.error).toBeUndefined()
    profileId = (created.result as { id?: string }).id ?? null
    expect(profileId).not.toBeNull()

    await expect(wire.call('operation.outcome', { mutation: identity })).resolves.toEqual({
      outcome: 'done',
      operation: 'profiles.create',
    })

    // THE REPLAY. A second copy of a mutation the record says ran is refused
    // before admission, and the refusal carries the answer so a client has
    // nothing left to guess.
    const replay = await wire.callWithMutation(
      'profiles.create',
      { type: 'ssh', name: `${name}-again`, options: { host: '127.0.0.1', port: 1, user: 'e2e' } },
      identity,
    )
    expect(replay.error?.code).toBe(-32005)
    expect(replay.error?.data).toEqual({
      reason: 'mutation-replayed',
      outcome: 'done',
      operation: 'profiles.create',
    })
    // And nothing was created by the refused copy.
    const profiles = (await wire.call('profiles.list', {})) as { name: string }[]
    expect(profiles.filter((p) => p.name === `${name}-again`)).toEqual([])

    // An identity the backend never saw says so, and names nothing.
    await expect(wire.call('operation.outcome', { mutation: 'e2e-never-seen' })).resolves.toEqual({
      outcome: 'unknown',
      operation: '',
    })
  } finally {
    wire.close()
    if (profileId !== null) {
      const endpoint = await resolveBackend(page).catch(() => null)
      if (endpoint !== null) {
        const cleanup = await openControlPlane(endpoint.port, endpoint.token).catch(() => null)
        if (cleanup !== null) {
          await cleanup.call('profiles.delete', { id: profileId }).catch(() => {})
          cleanup.close()
        }
      }
    }
  }
})

test('a mutation still running answers outcome-unknown, and stops saying so when it ends', async ({
  page,
}) => {
  const host = await silentSSHHost()
  const keyDir = mkdtempSync(join(tmpdir(), 'nocx-mutation-key-'))
  const keyPath = join(keyDir, 'id_rsa')
  const { privateKey } = generateKeyPairSync('rsa', {
    modulusLength: 2048,
    privateKeyEncoding: { type: 'pkcs1', format: 'pem' },
    publicKeyEncoding: { type: 'spki', format: 'pem' },
  })
  writeFileSync(keyPath, privateKey)
  chmodSync(keyPath, 0o600)
  const name = `${PROFILE_PREFIX}running-${Date.now()}`
  const identity = `e2e-mutation-running-${Date.now()}`
  let profileId: string | null = null
  const wire = await ready(page)
  try {
    const created = (await wire.call('profiles.create', {
      type: 'ssh',
      name,
      options: { host: '127.0.0.1', port: host.port, user: 'e2e', keyPath },
    })) as { id?: string }
    profileId = created.id ?? null
    expect(profileId).not.toBeNull()

    const request = wire.startMutation('connections.test', { profileId }, identity)
    // Past the first moment it could have answered: the record exists and the
    // work has not finished, which is the person's "checking what happened".
    await expect
      .poll(
        async () =>
          ((await wire.call('operation.outcome', { mutation: identity })) as OutcomeAnswer).outcome,
        { timeout: 20_000 },
      )
      .toBe('outcome-unknown')
    await expect(wire.call('operation.outcome', { mutation: identity })).resolves.toEqual({
      outcome: 'outcome-unknown',
      operation: 'connections.test',
    })

    // A second copy while the first runs is the double effect the record
    // exists to stop.
    const replay = await wire.callWithMutation('connections.test', { profileId }, identity)
    expect(replay.error?.code).toBe(-32005)

    // Withdraw it: the work stops, and the record stops saying "unknown".
    await wire.cancel(request)
    await wire.answer(request)
    await expect
      .poll(
        async () =>
          ((await wire.call('operation.outcome', { mutation: identity })) as OutcomeAnswer).outcome,
        { timeout: 40_000 },
      )
      .toBe('done')
  } finally {
    wire.close()
    for (const socket of host.held) socket.destroy()
    await new Promise<void>((resolve) => host.server.close(() => resolve()))
    rmSync(keyDir, { recursive: true, force: true })
    if (profileId !== null) {
      const endpoint = await resolveBackend(page).catch(() => null)
      if (endpoint !== null) {
        const cleanup = await openControlPlane(endpoint.port, endpoint.token).catch(() => null)
        if (cleanup !== null) {
          await cleanup.call('profiles.delete', { id: profileId }).catch(() => {})
          cleanup.close()
        }
      }
    }
  }
})

test('cancelling a mutation from its row keeps one of the three answers and a Cancel no more', async ({
  page,
}) => {
  const host = await silentSSHHost()
  const keyDir = mkdtempSync(join(tmpdir(), 'nocx-mutation-row-key-'))
  const keyPath = join(keyDir, 'id_rsa')
  const { privateKey } = generateKeyPairSync('rsa', {
    modulusLength: 2048,
    privateKeyEncoding: { type: 'pkcs1', format: 'pem' },
    publicKeyEncoding: { type: 'spki', format: 'pem' },
  })
  writeFileSync(keyPath, privateKey)
  chmodSync(keyPath, 0o600)
  const name = `${PROFILE_PREFIX}row-${Date.now()}`
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
        name,
        options: { host: '127.0.0.1', port: host.port, user: 'e2e', keyPath },
      })) as { id?: string }
      profileId = created.id ?? null
    } finally {
      wire.close()
    }
    expect(profileId).not.toBeNull()

    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
    await expect(
      page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }),
    ).toBeVisible({ timeout: 20_000 })

    await page.locator('button[data-view="operations"]').click()
    await page.getByRole('button', { name: `Test connection to ${name}` }).click()

    const row = page.locator('.ui-operation-row').filter({ hasText: 'Checking the connection' })
    await expect(row).toBeVisible({ timeout: 6_000 })
    await page.getByRole('button', { name: 'Cancel Checking the connection' }).click()

    // §8's three answers, and only those. The row stops offering a Cancel
    // because there is nothing left to stop, and it never becomes a retry.
    await expect(row).toContainText(/It did not start|It finished|Checking what happened/, {
      timeout: 30_000,
    })
    await expect(page.getByRole('button', { name: 'Cancel Checking the connection' })).toHaveCount(
      0,
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
