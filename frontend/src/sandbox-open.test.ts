import { describe, expect, it, vi } from 'vitest'
import { openSandboxedShell, SANDBOX_PATHS_KEY } from './sandbox-open'

function deps(overrides: Partial<Parameters<typeof openSandboxedShell>[0]> = {}) {
  return {
    getSnapshot: vi.fn().mockResolvedValue({
      values: { [SANDBOX_PATHS_KEY]: ['/a', '/b'] },
      revision: 7,
    }),
    openDirectory: vi.fn().mockResolvedValue({ path: '/workspace' }),
    showPermissions: vi.fn().mockResolvedValue({ add: ['/d'], remove: ['/b'] }),
    newSandboxedTab: vi.fn(),
    reportError: vi.fn(),
    ...overrides,
  }
}

describe('openSandboxedShell', () => {
  it('reads one fresh snapshot, shows the dialog, and forwards only revision + deltas', async () => {
    const d = deps()
    await openSandboxedShell(d)

    expect(d.getSnapshot).toHaveBeenCalledTimes(1)
    expect(d.openDirectory).toHaveBeenCalledTimes(1)
    expect(d.showPermissions).toHaveBeenCalledWith(
      expect.objectContaining({
        workspace: '/workspace',
        baseline: ['/a', '/b'],
      }),
    )
    expect(d.newSandboxedTab).toHaveBeenCalledWith('/workspace', {
      settingsRevision: 7,
      add: ['/d'],
      remove: ['/b'],
    })
    expect(d.reportError).not.toHaveBeenCalled()
  })

  it('never sends the baseline or an effective root', async () => {
    const d = deps()
    await openSandboxedShell(d)

    const launch = vi.mocked(d.newSandboxedTab).mock.calls[0][1] as {
      settingsRevision: number
      add: string[]
      remove: string[]
    }
    // The launch object carries only the revision and deltas — no baseline.
    expect(Object.keys(launch).sort()).toEqual(['add', 'remove', 'settingsRevision'])
  })

  it('a cancelled workspace picker creates no tab and no dialog', async () => {
    const d = deps({ openDirectory: vi.fn().mockResolvedValue({ path: '' }) })
    await openSandboxedShell(d)

    expect(d.showPermissions).not.toHaveBeenCalled()
    expect(d.newSandboxedTab).not.toHaveBeenCalled()
  })

  it('a cancelled permission dialog creates no tab', async () => {
    const d = deps({ showPermissions: vi.fn().mockResolvedValue(null) })
    await openSandboxedShell(d)

    expect(d.newSandboxedTab).not.toHaveBeenCalled()
  })

  it('a snapshot failure is reported as a typed toast and creates no tab', async () => {
    const d = deps({
      getSnapshot: vi.fn().mockRejectedValue(new Error('settings unavailable')),
    })
    await openSandboxedShell(d)

    expect(d.reportError).toHaveBeenCalledWith('settings unavailable')
    expect(d.newSandboxedTab).not.toHaveBeenCalled()
  })

  it('a non-array baseline reads as empty rather than throwing', async () => {
    const d = deps({
      getSnapshot: vi.fn().mockResolvedValue({
        values: { [SANDBOX_PATHS_KEY]: 'not-an-array' },
        revision: 3,
      }),
    })
    await openSandboxedShell(d)

    expect(d.showPermissions).toHaveBeenCalledWith(expect.objectContaining({ baseline: [] }))
  })
})
