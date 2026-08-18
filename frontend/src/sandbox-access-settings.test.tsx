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

const alternateEvent: SandboxAccessList['events'][number] = {
  ...event,
  id: 'fedcba9876543210fedcba9876543210',
  executable: '/opt/tools/python3',
  path: '/shared/archive/NOTES.md',
  directory: '/shared/archive',
  access: 'readOnly',
  operation: 'open',
  source: 'darwin-seatbelt-log',
}

const unknownProgramEvent: SandboxAccessList['events'][number] = {
  ...event,
  id: '11111111111111111111111111111111',
  executable: undefined,
  path: '/tmp/unattributed.log',
  directory: '/tmp',
  access: 'readOnly',
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
    expect(screen.getByText('/usr/bin/python3', { selector: 'dd' })).toBeTruthy()
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
    const keywords = await screen.findByRole<HTMLInputElement>('searchbox', {
      name: 'Filter by keywords',
    })
    fireEvent.input(keywords, { target: { value: 'report' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add global read-write' }))
    await waitFor(() => expect(resolve).toHaveBeenCalledWith(event.id, 'globalReadWrite'))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    expect(keywords.value).toBe('report')
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

  it('filters by exact application identity including unattributed events', async () => {
    const list = vi.fn().mockResolvedValue({
      events: [event, alternateEvent, unknownProgramEvent],
      revision: 1,
      lost: 0,
    })
    render(() => <SandboxAccessSettings client={client({ sandboxAccessList: list })} />)

    const application = await screen.findByRole<HTMLSelectElement>('combobox', {
      name: 'Filter by application',
    })
    expect(screen.getByRole('option', { name: '/usr/bin/python3' })).toBeTruthy()
    expect(screen.getByRole('option', { name: '/opt/tools/python3' })).toBeTruthy()
    expect(screen.getByRole('option', { name: 'Unknown program' })).toBeTruthy()

    fireEvent.change(application, { target: { value: JSON.stringify('/opt/tools/python3') } })
    expect(screen.getByText('/shared/archive/NOTES.md')).toBeTruthy()
    expect(screen.queryByText('/private/data/report.txt')).toBeNull()

    fireEvent.change(application, { target: { value: JSON.stringify(null) } })
    expect(screen.getByText('/tmp/unattributed.log')).toBeTruthy()
    expect(screen.queryByText('/shared/archive/NOTES.md')).toBeNull()
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('AND-matches case-insensitive keyword terms across stable metadata', async () => {
    const list = vi.fn().mockResolvedValue({
      events: [event, alternateEvent],
      revision: 1,
      lost: 0,
    })
    render(() => <SandboxAccessSettings client={client({ sandboxAccessList: list })} />)

    const keywords = await screen.findByRole<HTMLInputElement>('searchbox', {
      name: 'Filter by keywords',
    })
    fireEvent.input(keywords, { target: { value: '  PYTHON3   report  ' } })
    expect(screen.getByText('/private/data/report.txt')).toBeTruthy()
    expect(screen.queryByText('/shared/archive/NOTES.md')).toBeNull()

    fireEvent.input(keywords, { target: { value: 'seatbelt read only' } })
    expect(screen.getByText('/shared/archive/NOTES.md')).toBeTruthy()
    expect(screen.queryByText('/private/data/report.txt')).toBeNull()
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('composes filters and clears a no-match state without re-fetching', async () => {
    const list = vi.fn().mockResolvedValue({
      events: [event, alternateEvent],
      revision: 1,
      lost: 0,
    })
    render(() => <SandboxAccessSettings client={client({ sandboxAccessList: list })} />)

    const application = await screen.findByRole<HTMLSelectElement>('combobox', {
      name: 'Filter by application',
    })
    const keywords = screen.getByRole<HTMLInputElement>('searchbox', {
      name: 'Filter by keywords',
    })
    fireEvent.change(application, { target: { value: JSON.stringify('/opt/tools/python3') } })
    fireEvent.input(keywords, { target: { value: 'report' } })

    expect(screen.getByText('No access attempts match these filters')).toBeTruthy()
    expect(screen.queryByText('No denied access attempts')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(screen.getByText('/private/data/report.txt')).toBeTruthy()
    expect(screen.getByText('/shared/archive/NOTES.md')).toBeTruthy()
    expect(application.value).toBe('all')
    expect(keywords.value).toBe('')
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('keeps active filters across a path-free live reload', async () => {
    let notify: ((change: { revision: number }) => void) | undefined
    const subscribe = vi.fn((callback: (change: { revision: number }) => void) => {
      notify = callback
      return () => undefined
    })
    const refreshedEvent = { ...event, count: 3, lastSeen: '2026-08-18T10:00:02Z' }
    const list = vi
      .fn()
      .mockResolvedValueOnce({ events: [event, alternateEvent], revision: 1, lost: 0 })
      .mockResolvedValueOnce({ events: [refreshedEvent, alternateEvent], revision: 2, lost: 0 })
      .mockResolvedValueOnce({ events: [alternateEvent], revision: 3, lost: 0 })
    render(() => (
      <SandboxAccessSettings
        client={client({ sandboxAccessList: list, onSandboxAccessChanged: subscribe })}
      />
    ))

    const application = await screen.findByRole<HTMLSelectElement>('combobox', {
      name: 'Filter by application',
    })
    fireEvent.change(application, { target: { value: JSON.stringify('/usr/bin/python3') } })
    const keywords = await screen.findByRole<HTMLInputElement>('searchbox', {
      name: 'Filter by keywords',
    })
    fireEvent.input(keywords, { target: { value: 'report' } })
    expect(screen.queryByText('/shared/archive/NOTES.md')).toBeNull()

    notify?.({ revision: 2 })
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    expect(keywords.value).toBe('report')
    expect(application.value).toBe(JSON.stringify('/usr/bin/python3'))
    expect(screen.getByText('/private/data/report.txt')).toBeTruthy()
    expect(screen.queryByText('/shared/archive/NOTES.md')).toBeNull()

    notify?.({ revision: 3 })
    expect(await screen.findByText('No access attempts match these filters')).toBeTruthy()
    expect(list).toHaveBeenCalledTimes(3)
    expect(application.value).toBe(JSON.stringify('/usr/bin/python3'))
  })
})
