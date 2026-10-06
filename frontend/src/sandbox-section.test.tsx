// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { SandboxSection } from './sandbox-section'
import type { SandboxSettingsServices, SandboxPaneContext } from './sandbox-ui'
import type { SandboxStatusResult } from './generated/sandbox.status'
import type { SandboxProfileResult } from './generated/sandbox.profile.get'
import type { SandboxPreviewResult } from './generated/sandbox.preview'
import type { SandboxOperationResult } from './generated/sandbox.replace'

const context: SandboxPaneContext = {
  paneId: 'pane-captured',
  workspaceId: 'workspace-captured',
  kind: 'local',
  registered: Promise.resolve(true),
  isCurrent: () => true,
}
const status: SandboxStatusResult = {
  paneId: context.paneId,
  workspaceId: context.workspaceId,
  enabled: true,
  standardRevision: 7,
  workspaceRevision: 3,
  profileSource: 'workspace',
  availability: 'available',
  reason: '',
  source: { sessionId: 'session-source', instanceId: 'instance-source', sessionEpoch: 4 },
  head: {
    launchId: 'launch-current',
    mode: 'enforce',
    state: 'active',
    grantId: 55,
    policyDigest: 'digest-fixed',
    policyVersion: 1,
    enforcement: 'enforced',
    observer: 'unavailable',
  },
  preparingOperationId: '',
}
const profile: SandboxProfileResult = {
  standard: {
    schemaVersion: 1,
    revision: 7,
    enabled: true,
    readOnlyDirs: ['/standard-ro'],
    readWriteDirs: [],
  },
  workspace: {
    workspaceId: context.workspaceId,
    revision: 3,
    override: { readOnlyDirs: ['/workspace-ro'], readWriteDirs: ['/workspace-rw'] },
  },
  effective: { readOnlyDirs: ['/workspace-ro'], readWriteDirs: ['/workspace-rw'] },
  profileSource: 'workspace',
}
const policy: NonNullable<SandboxPreviewResult['policy']> = {
  version: 1,
  backend: 'linux-landlock',
  backendVersion: 9,
  workspaceId: context.workspaceId,
  workspaceRoot: '/workspace',
  standardRevision: 7,
  workspaceRevision: 3,
  shell: '/bin/sh',
  runner: '/runner',
  runtime: {
    root: '/runtime',
    home: '/runtime/home',
    config: '/runtime/config',
    data: '/runtime/data',
    cache: '/runtime/cache',
    state: '/runtime/state',
    temp: '/runtime/tmp',
  },
  roots: [],
}
const preview: SandboxPreviewResult = {
  operationId: 'operation-issued',
  confirmationId: 'confirmation-issued',
  expiresAt: '2026-10-06T00:00:00Z',
  workspaceId: context.workspaceId,
  mode: 'off',
  policy: null,
  policyDigest: '',
  policyVersion: 1,
}
const operation: SandboxOperationResult = {
  operationId: 'operation-issued',
  paneId: context.paneId,
  state: 'active',
  mode: 'enforce',
  reason: '',
  open: {
    sessionId: 'session-candidate',
    instanceId: 'instance-source',
    sessionEpoch: 5,
    workspaceId: context.workspaceId,
    cwd: '/workspace',
    desiredMode: 'raw',
    effectiveSize: { cols: 80, rows: 24, xpixel: 0, ypixel: 0 },
    parent: null,
    awaitsIntegration: false,
  },
}

afterEach(cleanup)

function services(overrides: Partial<SandboxSettingsServices['client']> = {}) {
  const client = {
    sandboxStatus: vi.fn().mockResolvedValue(status),
    sandboxProfile: vi.fn().mockResolvedValue(profile),
    sandboxUpdateProfile: vi.fn().mockResolvedValue(profile),
    sandboxResetProfile: vi.fn().mockResolvedValue(profile),
    sandboxPreview: vi.fn().mockResolvedValue(preview),
    sandboxReplace: vi.fn().mockResolvedValue(operation),
    sandboxCancel: vi.fn().mockResolvedValue({}),
    sandboxOperation: vi.fn().mockResolvedValue(operation),
    sandboxGrant: vi.fn().mockResolvedValue({
      launchId: 'launch-current',
      grantId: 55,
      mode: 'enforce',
      policy,
      policyDigest: 'digest-fixed',
      policyVersion: 1,
    }),
    sandboxAccessList: vi.fn().mockResolvedValue({
      paneId: context.paneId,
      launchId: 'launch-current',
      workspaceId: context.workspaceId,
      standardRevision: 7,
      workspaceRevision: 3,
      reason: '',
      inbox: {
        observer: 'unavailable',
        revision: 0,
        dropped: 0,
        discontinuity: false,
        total: 0,
        nextCursor: 0,
        records: [],
      },
    }),
    sandboxResolveAccess: vi.fn().mockResolvedValue({
      record: {
        id: '',
        revision: 0,
        executable: '',
        path: '',
        operation: '',
        access: 'unknown',
        pathKnown: false,
        source: 'linux-seccomp',
        precision: 'unknown',
        prediction: 'unknown',
        count: 0,
        state: 'unresolved',
        futureRevision: 0,
        proposal: null,
      },
      profile,
    }),
    onSandboxAccessChanged: vi.fn().mockReturnValue(() => {}),
    ...overrides,
  }
  const bindCandidate = vi.fn().mockResolvedValue(true)
  return {
    value: {
      client,
      workspaces: () => [{ id: 'workspace-captured', name: 'workspace' }],
      defaultWorkspaceId: () => 'workspace-default',
      bindCandidate,
      statusChanged: vi.fn(),
    } satisfies SandboxSettingsServices,
    client,
    bindCandidate,
  }
}

async function settledPage(svc: SandboxSettingsServices) {
  render(() => <SandboxSection services={svc} context={context} />)
  await screen.findByText('workspace')
}

describe('Sandbox Settings consumers', () => {
  it.each(['named', 'standard'] as const)(
    'preserves the %s editor draft while advancing its next CAS after diagnostic promotion',
    async (scope) => {
      const standardScope = scope === 'standard'
      const initialProfile: SandboxProfileResult = standardScope
        ? {
            ...profile,
            workspace: null,
            effective: profile.standard,
            profileSource: 'standard',
          }
        : profile
      const promoted: SandboxProfileResult = standardScope
        ? {
            standard: { ...profile.standard, revision: 8, readOnlyDirs: ['/promoted'] },
            workspace: { workspaceId: context.workspaceId, revision: 0, override: null },
            effective: { readOnlyDirs: ['/promoted'], readWriteDirs: [] },
            profileSource: 'standard',
          }
        : {
            ...profile,
            workspace: {
              workspaceId: context.workspaceId,
              revision: 4,
              override: { readOnlyDirs: ['/promoted'], readWriteDirs: [] },
            },
            effective: { readOnlyDirs: ['/promoted'], readWriteDirs: [] },
          }
      const resolvedRecord = {
        id: 'event-promote',
        revision: 2,
        executable: '/bin/tool',
        path: '/workspace/item',
        operation: 'openat',
        access: 'read' as const,
        pathKnown: true,
        source: 'linux-seccomp' as const,
        precision: 'attempted' as const,
        prediction: 'denied' as const,
        count: 1,
        state: 'future-policy' as const,
        futureRevision: standardScope ? 8 : 4,
        proposal: { directory: '/workspace', basis: 'directory' as const, missingTarget: false },
      }
      const initialPage = {
        paneId: context.paneId,
        launchId: 'launch-current',
        workspaceId: context.workspaceId,
        standardRevision: 7,
        workspaceRevision: standardScope ? 0 : 3,
        reason: '',
        inbox: {
          observer: 'active' as const,
          revision: 3,
          dropped: 0,
          discontinuity: false,
          total: 1,
          nextCursor: 0,
          records: [
            { ...resolvedRecord, revision: 1, state: 'unresolved' as const, futureRevision: 0 },
          ],
        },
      }
      const svc = services({
        sandboxProfile: vi.fn().mockResolvedValue(initialProfile),
        sandboxAccessList: vi
          .fn()
          .mockResolvedValueOnce(initialPage)
          .mockResolvedValue({
            ...initialPage,
            standardRevision: standardScope ? 8 : 7,
            workspaceRevision: standardScope ? 0 : 4,
            inbox: { ...initialPage.inbox, revision: 4, records: [resolvedRecord] },
          }),
        sandboxResolveAccess: vi
          .fn()
          .mockResolvedValue({ record: resolvedRecord, profile: promoted }),
      })
      if (standardScope) {
        svc.value.defaultWorkspaceId = () => context.workspaceId
        svc.value.workspaces = () => []
      }
      render(() => <SandboxSection services={svc.value} context={context} />)
      const root = await screen.findByLabelText<HTMLInputElement>('Корень только для чтения 1')
      fireEvent.input(root, { target: { value: '/draft-root' } })
      fireEvent.click(await screen.findByRole('button', { name: 'Разрешить чтение в будущем' }))
      const dialog = await screen.findByRole('dialog')
      fireEvent.click(
        dialog.querySelectorAll('button')[dialog.querySelectorAll('button').length - 1],
      )
      await waitFor(() => expect(document.querySelectorAll('dialog[open]')).toHaveLength(0))
      expect(root.value).toBe('/draft-root')
      fireEvent.click(screen.getByRole('button', { name: 'Сохранить профиль' }))
      await waitFor(() =>
        expect(svc.client.sandboxUpdateProfile).toHaveBeenCalledWith({
          ...(standardScope ? { enabled: true } : { workspaceId: context.workspaceId }),
          expectedRevision: standardScope ? 8 : 4,
          roots: {
            readOnlyDirs: ['/draft-root'],
            readWriteDirs: initialProfile.effective.readWriteDirs,
          },
        }),
      )
    },
  )
  it('advances the visible workspace clock after save and reset while restoring inherited roots', async () => {
    const updated: SandboxProfileResult = {
      ...profile,
      workspace: { ...profile.workspace!, revision: 4 },
    }
    const inherited: SandboxProfileResult = {
      ...profile,
      workspace: { ...profile.workspace!, revision: 5, override: null },
      effective: {
        readOnlyDirs: profile.standard.readOnlyDirs,
        readWriteDirs: profile.standard.readWriteDirs,
      },
      profileSource: 'standard',
    }
    const svc = services({
      sandboxUpdateProfile: vi.fn().mockResolvedValue(updated),
      sandboxResetProfile: vi.fn().mockResolvedValue(inherited),
    })
    await settledPage(svc.value)
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить профиль' }))
    await screen.findByText(/ревизия рабочей области 4/)
    fireEvent.click(screen.getByRole('button', { name: 'Сбросить к стандартному' }))
    await screen.findByText(/ревизия рабочей области 5/)
    expect(screen.getByLabelText<HTMLInputElement>('Корень только для чтения 1').value).toBe(
      '/standard-ro',
    )
    expect(screen.queryByLabelText('Корень для чтения и записи 1')).toBeNull()
  })

  it('keeps CAS refusal visible and does not claim the edited profile was saved', async () => {
    const refused = services({
      sandboxUpdateProfile: vi.fn().mockRejectedValue(new Error('stale_revision')),
    })
    await settledPage(refused.value)
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить профиль' }))
    expect(await screen.findByText('stale_revision')).toBeTruthy()
  })

  it('treats the hidden default workspace as standard rather than exposing a workspace override', async () => {
    const defaultContext = { ...context, workspaceId: 'default-workspace' }
    const standardProfile = {
      ...profile,
      workspace: null,
      effective: { readOnlyDirs: ['/standard-ro'], readWriteDirs: [] },
      profileSource: 'standard',
    }
    const svc = services({ sandboxProfile: vi.fn().mockResolvedValue(standardProfile) })
    render(() => <SandboxSection services={svc.value} context={defaultContext} />)
    const selector = await screen.findByRole('combobox', { name: 'Профиль песочницы' })
    expect((selector as HTMLSelectElement).value).toBe('')
    expect(screen.queryByRole('button', { name: 'Сбросить к стандартному' })).toBeNull()
  })

  it('cancels the exact issued preview with the separate cancel RPC', async () => {
    const svc = services()
    await settledPage(svc.value)
    fireEvent.click(screen.getByRole('button', { name: 'Удалить ограничение' }))
    await screen.findByRole('dialog')
    fireEvent.click(screen.getByRole('button', { name: 'Отмена' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('closes an expired confirmation while preserving its refusal and explicit state recheck', async () => {
    const svc = services({
      sandboxCancel: vi.fn().mockRejectedValue(new Error('confirmation_unknown')),
    })
    await settledPage(svc.value)
    fireEvent.click(screen.getByRole('button', { name: 'Удалить ограничение' }))
    await screen.findByRole('dialog')
    fireEvent.click(screen.getByRole('button', { name: 'Отмена' }))
    expect(await screen.findByText('confirmation_unknown')).toBeTruthy()
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(
      screen.getByRole('button', { name: 'Проверить состояние операции' }).hasAttribute('disabled'),
    ).toBe(false)
  })

  it('recovers a lost replace acknowledgement through the same operation ID and binds only the captured pane', async () => {
    const svc = services({
      sandboxPreview: vi.fn().mockResolvedValue({ ...preview, mode: 'enforce', policy }),
      sandboxReplace: vi.fn().mockRejectedValue(new Error('response lost')),
      sandboxOperation: vi.fn().mockResolvedValue(operation),
    })
    await settledPage(svc.value)
    fireEvent.click(screen.getByRole('button', { name: 'Применить / перезапустить' }))
    await screen.findByRole('dialog')
    fireEvent.click(screen.getByRole('button', { name: 'Подтвердить' }))
    await waitFor(() =>
      expect(svc.client.sandboxOperation).toHaveBeenCalledWith({
        operationId: preview.operationId,
      }),
    )
    expect(svc.client.sandboxPreview).toHaveBeenCalledTimes(1)
    expect(svc.bindCandidate).toHaveBeenCalledWith(context, operation)
    expect(svc.client.sandboxReplace).toHaveBeenCalledWith({
      operationId: preview.operationId,
      confirmationId: preview.confirmationId,
    })
  })

  it('refuses confirmation if the captured pane drifts while its preview is open', async () => {
    const [current, setCurrent] = createSignal(true)
    const drifted = { ...context, isCurrent: current }
    const svc = services()
    render(() => <SandboxSection services={svc.value} context={drifted} />)
    await screen.findByText('workspace')
    fireEvent.click(screen.getByRole('button', { name: 'Удалить ограничение' }))
    await screen.findByRole('dialog')
    setCurrent(false)
    expect(screen.getByRole('button', { name: 'Подтвердить' }).hasAttribute('disabled')).toBe(true)
    expect(svc.client.sandboxReplace).not.toHaveBeenCalled()
    expect(svc.bindCandidate).not.toHaveBeenCalled()
  })
})
