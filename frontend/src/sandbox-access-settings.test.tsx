// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SandboxAccessSettings, type SandboxAccessClient } from './sandbox-access-settings'
import type { SandboxAccessList } from './ipc'

afterEach(() => cleanup())

const event: SandboxAccessList['events'][number] = {
  id: '0123456789abcdef0123456789abcdef',
  sessionId: 'session',
  instanceId: 'instance',
  sessionEpoch: 1,
  shell: '/bin/zsh',
  executable: '/usr/bin/python3',
  path: '/private/data/report.txt',
  directory: '/private/data',
  canGrant: true,
  access: 'readWrite',
  operation: 'openat',
  source: 'linux-seccomp-user-notify',
  firstSeen: '2026-08-18T10:00:00Z',
  lastSeen: '2026-08-18T10:00:01Z',
  count: 2,
  state: 'pending',
}

function client(overrides: Partial<SandboxAccessClient> = {}): SandboxAccessClient {
  return {
    sandboxAccessStatus: vi.fn().mockResolvedValue({
      available: true,
      platform: 'linux',
      backend: 'linux-seccomp-user-notify',
      lost: 0,
    }),
    sandboxAccessList: vi.fn().mockResolvedValue({ events: [event], revision: 1, lost: 0 }),
    sandboxAccessResolve: vi.fn().mockResolvedValue({ ...event, state: 'granted' }),
    onSandboxAccessChanged: vi.fn().mockReturnValue(() => undefined),
    ...overrides,
  }
}

describe('SandboxAccessSettings', () => {
  it('shows attribution and all three explicit decisions', async () => {
    render(() => <SandboxAccessSettings client={client()} />)
    expect(await screen.findByText('/private/data/report.txt')).toBeTruthy()
    expect(screen.getByText('/usr/bin/python3')).toBeTruthy()
    expect(screen.getByText('/bin/zsh')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Add global read-only' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Add global read-write' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeTruthy()
    expect(screen.getByText(/read-only rule will not satisfy this write attempt/i)).toBeTruthy()
  })

  it('resolves by event id and refreshes', async () => {
    const resolve = vi.fn().mockResolvedValue({ ...event, state: 'granted' })
    const list = vi
      .fn()
      .mockResolvedValueOnce({ events: [event], revision: 1, lost: 0 })
      .mockResolvedValueOnce({ events: [{ ...event, state: 'granted' }], revision: 2, lost: 0 })
    const api = client({ sandboxAccessResolve: resolve, sandboxAccessList: list })
    render(() => <SandboxAccessSettings client={api} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add global read-write' }))
    await waitFor(() => expect(resolve).toHaveBeenCalledWith(event.id, 'globalReadWrite'))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
  })

  it('keeps actions visible but disables unsafe grants', async () => {
    const blocked = {
      ...event,
      directory: '',
      canGrant: false,
      grantReason: 'Target directory no longer exists.',
    }
    render(() => (
      <SandboxAccessSettings
        client={client({
          sandboxAccessList: vi.fn().mockResolvedValue({ events: [blocked], revision: 1, lost: 0 }),
        })}
      />
    ))
    const readOnly = await screen.findByRole('button', { name: 'Add global read-only' })
    expect(readOnly.hasAttribute('disabled')).toBe(true)
    expect(
      screen.getByRole('button', { name: 'Add global read-write' }).hasAttribute('disabled'),
    ).toBe(true)
    expect(screen.getByRole('button', { name: 'Dismiss' }).hasAttribute('disabled')).toBe(false)
    expect(screen.getByText('Target directory no longer exists.')).toBeTruthy()
  })

  it('shows honest monitor unavailability', async () => {
    render(() => (
      <SandboxAccessSettings
        client={client({
          sandboxAccessStatus: vi.fn().mockResolvedValue({
            available: false,
            platform: 'linux',
            reason: 'seccomp-user-notify-unavailable',
            detail: 'Sandbox enforcement is active; denied-access observation is unavailable.',
            lost: 3,
          }),
          sandboxAccessList: vi.fn().mockResolvedValue({ events: [], revision: 0, lost: 3 }),
        })}
      />
    ))
    expect(await screen.findByText(/denied-access observation is unavailable/i)).toBeTruthy()
    expect(screen.getByText(/3 events were dropped/i)).toBeTruthy()
  })
})
