// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import type { SandboxAccessChanged } from './generated/sandbox.access.changed'
import { SandboxDiagnosticsSection } from './sandbox-diagnostics-section'
import type { SandboxSettingsServices, SandboxPaneContext } from './sandbox-ui'
import type { SandboxAccessListResult } from './generated/sandbox.access.list'
import type { DiagnosticRecord } from './generated/sandbox.access.record'
import type { SandboxProfileResult } from './generated/sandbox.profile.get'
import { RpcError } from './dispatcher'

const captured: SandboxPaneContext = {
  paneId: 'pane-fixed',
  workspaceId: 'workspace-fixed',
  kind: 'local',
  registered: Promise.resolve(true),
  isCurrent: () => true,
}
const profile: SandboxProfileResult = {
  standard: { schemaVersion: 1, revision: 12, enabled: true, readOnlyDirs: [], readWriteDirs: [] },
  workspace: { workspaceId: captured.workspaceId, revision: 6, override: null },
  effective: { readOnlyDirs: [], readWriteDirs: [] },
  profileSource: 'standard',
}
const proposal = (id: string, values: Partial<DiagnosticRecord> = {}): DiagnosticRecord => ({
  id,
  revision: 4,
  executable: '/usr/bin/tool',
  path: '/work/item',
  operation: 'openat',
  access: 'read',
  pathKnown: true,
  source: 'linux-seccomp',
  precision: 'attempted',
  prediction: 'denied',
  count: 2,
  state: 'unresolved',
  futureRevision: 0,
  proposal: { directory: '/work', basis: 'directory', missingTarget: false },
  ...values,
})
const page = (
  records: DiagnosticRecord[],
  total = records.length,
  nextCursor = 0,
): SandboxAccessListResult => ({
  paneId: captured.paneId,
  launchId: 'launch-fixed',
  workspaceId: captured.workspaceId,
  standardRevision: 12,
  workspaceRevision: 6,
  reason: '',
  inbox: {
    observer: 'active',
    revision: 9,
    dropped: 3,
    discontinuity: true,
    total,
    nextCursor,
    records,
  },
})

afterEach(cleanup)

function setup(
  list: (cursor: number, limit: number) => Promise<SandboxAccessListResult>,
  defaultWorkspaceId = 'workspace-default',
) {
  let notice: ((fact: SandboxAccessChanged) => void) | undefined
  const client = {
    sandboxAccessList: vi.fn(({ cursor, limit }: { cursor: number; limit: number }) =>
      list(cursor, limit),
    ),
    sandboxResolveAccess: vi.fn().mockResolvedValue({
      record: proposal('event-a', { state: 'future-policy', futureRevision: 13 }),
      profile,
    }),
    sandboxProfile: vi.fn().mockResolvedValue(profile),
    onSandboxAccessChanged: vi.fn((handler: (fact: SandboxAccessChanged) => void) => {
      notice = handler
      return () => {
        notice = undefined
      }
    }),
  }
  const services = {
    client,
    workspaces: () => [{ id: captured.workspaceId, name: 'Work' }],
    defaultWorkspaceId: () => defaultWorkspaceId,
  } as unknown as SandboxSettingsServices
  render(() => (
    <SandboxDiagnosticsSection
      services={services}
      context={captured}
      launchId="launch-fixed"
      onProfileResolved={() => {}}
    />
  ))
  return { client, notify: (fact: SandboxAccessChanged) => notice?.(fact) }
}

describe('Sandbox diagnostic inbox consumer', () => {
  it('uses captured clocks and event revision for a precise future-policy action; unknown proposals cannot be allowed', async () => {
    const unknown = proposal('event-unknown', {
      pathKnown: false,
      access: 'unknown',
      proposal: null,
    })
    const valid = proposal('event-a')
    let reads = 0
    const service = setup(() => {
      reads++
      const snapshot = page([unknown, valid])
      return Promise.resolve(
        reads === 1 ? snapshot : { ...snapshot, standardRevision: 99, workspaceRevision: 88 },
      )
    })
    await waitFor(() => expect(document.querySelectorAll('article')).toHaveLength(2))
    const articles = document.querySelectorAll('article')
    expect(articles[0].querySelectorAll('button')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Разрешить чтение в будущем' }))
    const dialog = await screen.findByRole('dialog')
    service.notify({
      paneId: 'pane-fixed',
      launchId: 'launch-fixed',
      revision: 10,
      dropped: 3,
      observer: 'active',
      total: 2,
    })
    await waitFor(() => expect(service.client.sandboxAccessList).toHaveBeenCalledTimes(2))
    fireEvent.click(dialog.querySelectorAll('button')[dialog.querySelectorAll('button').length - 1])
    await waitFor(() =>
      expect(service.client.sandboxResolveAccess).toHaveBeenCalledWith({
        paneId: 'pane-fixed',
        launchId: 'launch-fixed',
        eventId: 'event-a',
        eventRevision: 4,
        decision: 'allow-ro',
        expectedStandardRevision: 12,
        expectedWorkspaceRevision: 6,
      }),
    )
  })
  it('does not treat a pending resolution as saved or repeat its write', async () => {
    const row = proposal('event-pending')
    const pendingRow = { ...row, state: 'pending' as const }
    const profileResolved = vi.fn()
    const list = vi
      .fn()
      .mockResolvedValueOnce(page([row]))
      .mockResolvedValue(page([pendingRow]))
    const client = {
      sandboxAccessList: list,
      sandboxResolveAccess: vi.fn().mockResolvedValue({ record: pendingRow, profile }),
      sandboxProfile: vi.fn().mockResolvedValue(profile),
      onSandboxAccessChanged: vi.fn(() => () => {}),
    }
    const services = {
      client,
      workspaces: () => [{ id: captured.workspaceId, name: 'Work' }],
      defaultWorkspaceId: () => 'workspace-default',
    } as unknown as SandboxSettingsServices
    render(() => (
      <SandboxDiagnosticsSection
        services={services}
        context={captured}
        launchId="launch-fixed"
        onProfileResolved={profileResolved}
      />
    ))
    await waitFor(() => expect(document.querySelectorAll('article')).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Разрешить чтение в будущем' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(dialog.querySelectorAll('button')[dialog.querySelectorAll('button').length - 1])
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    expect(client.sandboxResolveAccess).toHaveBeenCalledTimes(1)
    expect(client.sandboxProfile).toHaveBeenCalledTimes(1)
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Разрешить чтение в будущем' })).toBeNull(),
    )
  })
  it('withholds grants while the default workspace identity is unavailable', async () => {
    setup(() => Promise.resolve(page([proposal('event-scope')])), '')
    await waitFor(() => expect(document.querySelectorAll('article')).toHaveLength(1))
    expect(screen.queryByRole('button', { name: 'Разрешить чтение в будущем' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Разрешить чтение и запись в будущем' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Отклонить предложение' })).toBeTruthy()
  })

  it('pages using the returned cursor and refreshes only for the captured pane and launch notice', async () => {
    const first = page([proposal('event-first')], 201, 200)
    const second = page([proposal('event-last')], 201, 0)
    const service = setup((cursor) => Promise.resolve(cursor === 0 ? first : second))
    await waitFor(() => expect(document.querySelectorAll('article')).toHaveLength(1))
    service.notify({
      paneId: 'another-pane',
      launchId: 'launch-fixed',
      revision: 10,
      dropped: 3,
      observer: 'active',
      total: 201,
    })
    expect(service.client.sandboxAccessList).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Следующая' }))
    await waitFor(() =>
      expect(document.querySelector('article')?.textContent).toContain('event-last'),
    )
    expect(service.client.sandboxAccessList).toHaveBeenLastCalledWith({
      paneId: 'pane-fixed',
      launchId: 'launch-fixed',
      cursor: 200,
      limit: 200,
    })
    service.notify({
      paneId: 'pane-fixed',
      launchId: 'launch-fixed',
      revision: 11,
      dropped: 3,
      observer: 'active',
      total: 201,
    })
    await waitFor(() => expect(service.client.sandboxAccessList).toHaveBeenCalledTimes(3))
    expect(service.client.sandboxAccessList).toHaveBeenLastCalledWith({
      paneId: 'pane-fixed',
      launchId: 'launch-fixed',
      cursor: 200,
      limit: 200,
    })
  })

  it.each(['stale_revision', 'profile_conflict'])(
    'keeps %s visible after refresh and requires a new confirmation',
    async (reason) => {
      const initial = page([proposal('event-conflict')])
      const refreshed = {
        ...initial,
        standardRevision: initial.standardRevision + 1,
        inbox: { ...initial.inbox, records: [proposal('event-conflict', { revision: 2 })] },
      }
      let reads = 0
      const service = setup(() => Promise.resolve(++reads === 1 ? initial : refreshed))
      service.client.sandboxResolveAccess.mockRejectedValue(
        new RpcError(reason, -32000, { reason }),
      )
      fireEvent.click(await screen.findByRole('button', { name: 'Разрешить чтение в будущем' }))
      const dialog = await screen.findByRole('dialog')
      fireEvent.click(
        dialog.querySelectorAll('button')[dialog.querySelectorAll('button').length - 1],
      )
      await waitFor(() => expect(service.client.sandboxAccessList).toHaveBeenCalledTimes(2))
      expect(screen.getByText(reason)).toBeTruthy()
      expect(document.querySelectorAll('dialog[open]')).toHaveLength(0)
      expect(service.client.sandboxResolveAccess).toHaveBeenCalledTimes(1)
      fireEvent.click(screen.getByRole('button', { name: 'Разрешить чтение в будущем' }))
      expect(await screen.findByRole('dialog')).toBeTruthy()
      expect(service.client.sandboxResolveAccess).toHaveBeenCalledTimes(1)
    },
  )

  it('discards a late page after the captured launch moves away', async () => {
    let resolvePage: ((result: SandboxAccessListResult) => void) | undefined
    const pendingPage = new Promise<SandboxAccessListResult>((resolve) => {
      resolvePage = resolve
    })
    let isCurrent = true
    const current = { ...captured, isCurrent: () => isCurrent }
    const list = vi.fn(() => pendingPage)
    const services = {
      client: {
        sandboxAccessList: list,
        sandboxResolveAccess: vi.fn(),
        onSandboxAccessChanged: vi.fn(() => () => {}),
      },
      workspaces: () => [],
      defaultWorkspaceId: () => 'workspace-default',
    } as unknown as SandboxSettingsServices
    render(() => (
      <SandboxDiagnosticsSection
        services={services}
        context={current}
        launchId="launch-fixed"
        onProfileResolved={() => {}}
      />
    ))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    isCurrent = false
    if (!resolvePage) throw new Error('page resolver was not initialized')
    resolvePage(page([proposal('late-event')]))
    await pendingPage
    await Promise.resolve()
    await Promise.resolve()
    expect(screen.queryByText(/late-event/)).toBeNull()
  })
})
