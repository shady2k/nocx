import { describe, expect, it, vi } from 'vitest'
import { openSandboxedShell } from './sandbox-open'

function deps(overrides: Partial<Parameters<typeof openSandboxedShell>[0]> = {}) {
  return {
    getSnapshot: vi.fn().mockResolvedValue({ values: {}, revision: 7 }),
    getProfile: vi.fn().mockResolvedValue({
      source: 'workspace' as const,
      revision: 4,
      writablePaths: ['/a', '/b'],
      readOnlyPaths: ['/r1'],
    }),
    openDirectory: vi.fn().mockResolvedValue({ path: '/workspace' }),
    showPermissions: vi.fn().mockResolvedValue({
      addWritable: ['/d'],
      removeWritable: ['/b'],
      addReadOnly: ['/r2'],
      removeReadOnly: ['/r1'],
    }),
    newSandboxedTab: vi.fn(),
    reportError: vi.fn(),
    ...overrides,
  }
}

describe('openSandboxedShell', () => {
  it('reads one fresh profile, shows both defaults, and forwards only revisions + deltas', async () => {
    const d = deps()
    await openSandboxedShell(d, { paneId: 'pane-1' })

    expect(d.getSnapshot).toHaveBeenCalledTimes(1)
    expect(d.getProfile).toHaveBeenCalledWith('pane-1')
    expect(d.openDirectory).toHaveBeenCalledTimes(1)
    expect(d.showPermissions).toHaveBeenCalledWith(
      expect.objectContaining({
        workspace: '/workspace',
        baselineWritable: ['/a', '/b'],
        baselineReadOnly: ['/r1'],
      }),
    )
    expect(d.newSandboxedTab).toHaveBeenCalledWith('/workspace', {
      settingsRevision: 7,
      profileRevision: 4,
      addWritable: ['/d'],
      removeWritable: ['/b'],
      addReadOnly: ['/r2'],
      removeReadOnly: ['/r1'],
    })
    expect(d.reportError).not.toHaveBeenCalled()
  })

  it('uses a verified workspace override without opening the initial picker', async () => {
    const d = deps()
    await openSandboxedShell(d, { workspace: '/verified/project', paneId: 'pane-1' })

    expect(d.openDirectory).not.toHaveBeenCalled()
    expect(d.showPermissions).toHaveBeenCalledWith(
      expect.objectContaining({ workspace: '/verified/project' }),
    )
    expect(d.newSandboxedTab).toHaveBeenCalledWith('/verified/project', expect.any(Object))
  })

  it('never sends either baseline or an effective root', async () => {
    const d = deps()
    await openSandboxedShell(d, { paneId: 'pane-1' })

    const launch = vi.mocked(d.newSandboxedTab).mock.calls[0][1] as Record<string, unknown>
    // The launch object carries only the revision and four deltas — no baseline.
    expect(Object.keys(launch).sort()).toEqual([
      'addReadOnly',
      'addWritable',
      'profileRevision',
      'removeReadOnly',
      'removeWritable',
      'settingsRevision',
    ])
  })

  it('a cancelled workspace picker creates no tab and no dialog', async () => {
    const d = deps({ openDirectory: vi.fn().mockResolvedValue({ path: '' }) })
    await openSandboxedShell(d, { paneId: 'pane-1' })

    expect(d.showPermissions).not.toHaveBeenCalled()
    expect(d.newSandboxedTab).not.toHaveBeenCalled()
  })

  it('a cancelled permission dialog creates no tab', async () => {
    const d = deps({ showPermissions: vi.fn().mockResolvedValue(null) })
    await openSandboxedShell(d, { paneId: 'pane-1' })

    expect(d.newSandboxedTab).not.toHaveBeenCalled()
  })

  it('a snapshot failure is reported as a typed toast and creates no tab', async () => {
    const d = deps({
      getSnapshot: vi.fn().mockRejectedValue(new Error('settings unavailable')),
    })
    await openSandboxedShell(d, { paneId: 'pane-1' })

    expect(d.reportError).toHaveBeenCalledWith('settings unavailable')
    expect(d.newSandboxedTab).not.toHaveBeenCalled()
  })

  it('a standard profile sends null profileRevision', async () => {
    const d = deps({
      getProfile: vi.fn().mockResolvedValue({
        source: 'standard',
        revision: 3,
        writablePaths: [],
        readOnlyPaths: [],
      }),
    })
    await openSandboxedShell(d, { paneId: 'pane-1' })

    expect(d.showPermissions).toHaveBeenCalledWith(
      expect.objectContaining({ baselineWritable: [], baselineReadOnly: [] }),
    )
    expect(d.newSandboxedTab).toHaveBeenCalledWith(
      '/workspace',
      expect.objectContaining({ profileRevision: null }),
    )
  })
})
