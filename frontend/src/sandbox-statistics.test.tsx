// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  SandboxStatistics,
  type SandboxStatisticsClient,
  type SandboxStatisticsDeps,
} from './sandbox-statistics'
import type { SandboxStatus, SandboxAccessStatus, SandboxGrantGet, SandboxProfileGet } from './ipc'

afterEach(() => cleanup())

// ── Fixtures ──────────────────────────────────────────────────────────

const enforcementAvailable: SandboxStatus = {
  available: true,
  backend: 'landlock',
  reason: 'Landlock is available',
  detail: 'Kernel 5.13+ Landlock LSM enforces filesystem restrictions.',
  abi: 3,
}

const enforcementUnavailable: SandboxStatus = {
  available: false,
  backend: 'unsupported',
  reason: 'Kernel too old',
  detail: 'Landlock requires Linux 5.13 or later.',
}

const observerAvailable: SandboxAccessStatus = {
  available: true,
  platform: 'linux',
  backend: 'linux-seccomp-user-notify',
  detail: 'seccomp-user-notify observer is active.',
  lost: 0,
}

const observerUnavailable: SandboxAccessStatus = {
  available: false,
  platform: 'linux',
  reason: 'seccomp-user-notify-unavailable',
  detail: 'Sandbox enforcement is active; denied-access observation is unavailable.',
  lost: 5,
}

const grant: SandboxGrantGet = {
  issuedAt: 1724457600000,
  realized: {
    backend: 'landlock',
    workspace: '/home/user/project',
    writableRoots: ['/home/user/project', '/tmp/build'],
    readOnlyRoots: ['/usr/share/data'],
    homeProjections: [{ hostPath: '/home/user', relativePath: '.nocx-home' }],
  },
  provenance: {
    workspaceId: 'ws-1',
    profileSource: 'workspace',
    profileRevision: 3,
  },
}

const profileInherited: SandboxProfileGet = {
  workspaceId: 'ws-1',
  source: 'standard',
  revision: 5,
  inherited: true,
  writablePaths: [],
  readOnlyPaths: [],
}

const profileExplicit: SandboxProfileGet = {
  workspaceId: 'ws-1',
  source: 'workspace',
  revision: 3,
  inherited: false,
  writablePaths: ['/tmp/build'],
  readOnlyPaths: ['/usr/share/data'],
}

// ── Helpers ───────────────────────────────────────────────────────────

function makeClient(overrides: Partial<SandboxStatisticsClient> = {}): SandboxStatisticsClient {
  return {
    sandboxStatus: vi.fn().mockResolvedValue(enforcementAvailable),
    sandboxAccessStatus: vi.fn().mockResolvedValue(observerAvailable),
    sandboxAccessList: vi.fn().mockResolvedValue({ events: [], revision: 0, lost: 0 }),
    sandboxAccessResolve: vi.fn(() => Promise.reject(new Error('not used'))),
    onSandboxAccessChanged: vi.fn().mockReturnValue(() => undefined),
    sandboxProfileGet: vi.fn().mockResolvedValue(profileInherited),
    sandboxProfileSet: vi.fn().mockResolvedValue({
      workspaceId: 'ws-1',
      revision: 4,
      writablePaths: ['/tmp/build'],
      readOnlyPaths: [],
    }),
    sandboxProfileDelete: vi.fn().mockResolvedValue({ workspaceId: 'ws-1' }),
    sandboxGrantGet: vi.fn().mockResolvedValue(grant),
    ...overrides,
  }
}

function makeDeps(overrides: Partial<SandboxStatisticsDeps> = {}): SandboxStatisticsDeps {
  return {
    activePaneId: () => 'pane-1',
    relaunch: vi.fn(),
    openDirectory: vi.fn().mockResolvedValue({ path: '' }),
    ...overrides,
  }
}

// ── Tests ─────────────────────────────────────────────────────────────

describe('SandboxStatistics', () => {
  it('renders all four section titles', async () => {
    render(() => <SandboxStatistics client={makeClient()} deps={makeDeps()} />)
    expect(await screen.findByText('Enforcement status')).toBeTruthy()
    expect(screen.getByText('Source tab grant')).toBeTruthy()
    expect(screen.getByText('Source workspace profile')).toBeTruthy()
    expect(screen.getByText('Denied access')).toBeTruthy()
  })

  it('shows enforcement active with backend name', async () => {
    render(() => <SandboxStatistics client={makeClient()} deps={makeDeps()} />)
    expect(await screen.findByText(/Sandbox enforcement active/)).toBeTruthy()
    // "landlock" appears in both enforcement status and grant realized backend
    expect(screen.getAllByText(/landlock/).length).toBeGreaterThanOrEqual(2)
  })

  it('shows enforcement unavailable', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({ sandboxStatus: vi.fn().mockResolvedValue(enforcementUnavailable) })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText(/Sandbox enforcement unavailable/)).toBeTruthy()
  })

  it('shows observer active', async () => {
    render(() => <SandboxStatistics client={makeClient()} deps={makeDeps()} />)
    expect(await screen.findByText('Denied-access observer active')).toBeTruthy()
  })

  it('shows observer unavailable with lost events', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({
          sandboxAccessStatus: vi.fn().mockResolvedValue(observerUnavailable),
        })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText(/Denied-access observer unavailable/)).toBeTruthy()
    // "5 events were dropped" appears in both enforcement and denied access sections
    expect(screen.getAllByText(/5 events were dropped/).length).toBeGreaterThanOrEqual(1)
  })

  it('shows grant workspace and realized roots', async () => {
    render(() => <SandboxStatistics client={makeClient()} deps={makeDeps()} />)
    expect(await screen.findByText(/Workspace: \/home\/user\/project/)).toBeTruthy()
    expect(screen.getByText(/\/home\/user\/project, \/tmp\/build/)).toBeTruthy()
    expect(screen.getByText(/\/usr\/share\/data/)).toBeTruthy()
  })

  it('shows grant provenance with revision', async () => {
    render(() => <SandboxStatistics client={makeClient()} deps={makeDeps()} />)
    expect(await screen.findByText(/Workspace profile \(revision 3\)/)).toBeTruthy()
  })

  it('shows stale grant warning when profile revision differs', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({
          sandboxProfileGet: vi.fn().mockResolvedValue({ ...profileExplicit, revision: 5 }),
        })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText('Grant is stale')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Relaunch with updated profile' })).toBeTruthy()
  })

  it('calls relaunch when stale grant button is clicked', async () => {
    const relaunch = vi.fn()
    render(() => (
      <SandboxStatistics
        client={makeClient({
          sandboxProfileGet: vi.fn().mockResolvedValue({ ...profileExplicit, revision: 5 }),
        })}
        deps={makeDeps({ relaunch })}
      />
    ))
    const button = await screen.findByRole('button', { name: 'Relaunch with updated profile' })
    button.click()
    expect(relaunch).toHaveBeenCalled()
  })

  it('shows inherited profile status', async () => {
    render(() => <SandboxStatistics client={makeClient()} deps={makeDeps()} />)
    expect(await screen.findByText('Inheriting standard profile')).toBeTruthy()
  })

  it('creates an explicit profile from inherited defaults with revision zero', async () => {
    const profileSet = vi.fn().mockResolvedValue({
      workspaceId: 'ws-1',
      revision: 1,
      writablePaths: ['/new-write'],
      readOnlyPaths: [],
    })
    render(() => (
      <SandboxStatistics
        client={makeClient({ sandboxProfileSet: profileSet })}
        deps={makeDeps({
          openDirectory: vi.fn().mockResolvedValue({ path: '/new-write' }),
        })}
      />
    ))

    ;(await screen.findByRole('button', { name: 'Add writable folder' })).click()
    ;(await screen.findByRole('button', { name: 'Save profile' })).click()

    await waitFor(() => expect(profileSet).toHaveBeenCalledWith('ws-1', 0, ['/new-write'], []))
  })

  it('shows explicit profile status', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({
          sandboxProfileGet: vi.fn().mockResolvedValue(profileExplicit),
        })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText(/Explicit workspace profile for ws-1/)).toBeTruthy()
  })

  it('shows delete profile button when explicit', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({
          sandboxProfileGet: vi.fn().mockResolvedValue(profileExplicit),
        })}
        deps={makeDeps()}
      />
    ))
    expect(
      await screen.findByRole('button', { name: 'Delete profile (inherit standard)' }),
    ).toBeTruthy()
  })

  it('does not show delete button when inherited', async () => {
    render(() => <SandboxStatistics client={makeClient()} deps={makeDeps()} />)
    await screen.findByText('Inheriting standard profile')
    expect(screen.queryByRole('button', { name: 'Delete profile (inherit standard)' })).toBeNull()
  })

  it('deletes profile and reloads', async () => {
    const profileGet = vi
      .fn()
      .mockResolvedValueOnce(profileExplicit)
      .mockResolvedValueOnce(profileInherited)
    const profileDelete = vi.fn().mockResolvedValue({ workspaceId: 'ws-1' })
    render(() => (
      <SandboxStatistics
        client={makeClient({ sandboxProfileGet: profileGet, sandboxProfileDelete: profileDelete })}
        deps={makeDeps()}
      />
    ))
    const deleteBtn = await screen.findByRole('button', {
      name: 'Delete profile (inherit standard)',
    })
    deleteBtn.click()
    await waitFor(() => expect(profileDelete).toHaveBeenCalledWith('ws-1', 3))
    await waitFor(() => expect(screen.getByText('Inheriting standard profile')).toBeTruthy())
  })

  it('shows retry button on enforcement load failure', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({
          sandboxStatus: vi.fn().mockRejectedValue(new Error('fail')),
          sandboxAccessStatus: vi.fn().mockRejectedValue(new Error('fail')),
        })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText("Couldn't load enforcement status")).toBeTruthy()
    // Multiple Retry buttons: enforcement section and denied access section
    expect(screen.getAllByRole('button', { name: 'Retry' }).length).toBeGreaterThanOrEqual(1)
  })

  it('shows retry button on grant load failure', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({ sandboxGrantGet: vi.fn().mockRejectedValue(new Error('fail')) })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText("Couldn't load grant")).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })

  it('shows retry button on profile load failure', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({ sandboxProfileGet: vi.fn().mockRejectedValue(new Error('fail')) })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText("Couldn't load profile")).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })

  it('shows no-grant empty state when no pane', async () => {
    render(() => (
      <SandboxStatistics
        client={makeClient({ sandboxGrantGet: vi.fn().mockResolvedValue(null) })}
        deps={makeDeps()}
      />
    ))
    expect(await screen.findByText('No sandbox grant')).toBeTruthy()
  })

  it('shows no-pane empty state for profile', async () => {
    render(() => (
      <SandboxStatistics client={makeClient()} deps={makeDeps({ activePaneId: () => null })} />
    ))
    await waitFor(() => expect(screen.getByText('No source terminal')).toBeTruthy())
  })
})
