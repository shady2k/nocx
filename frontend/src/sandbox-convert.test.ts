import { describe, expect, it, vi, type Mock } from 'vitest'
import type { ActiveOrigin } from './pane-content'
import {
  createSandboxConvertController,
  type SandboxConversionDeps,
  type SandboxPaneManagerPort,
  type SandboxPane,
} from './sandbox-convert'
import type { SandboxStatus } from './ipc'

const localOrigin: ActiveOrigin = {
  paneId: 1,
  sessionId: 'session-1',
  kind: 'local',
  cwd: '/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const oldPane: SandboxPane = { id: 1, wireId: 'pane-wire-1' }
const newPane: SandboxPane = { id: 2, wireId: 'pane-wire-2' }
interface SandboxPaneManagerFake extends SandboxPaneManagerPort {
  activeOrigin: Mock<SandboxPaneManagerPort['activeOrigin']>
  paneOf: Mock<SandboxPaneManagerPort['paneOf']>
  tabOf: Mock<SandboxPaneManagerPort['tabOf']>
  captureConversionTranscript: Mock<SandboxPaneManagerPort['captureConversionTranscript']>
  newLocalPaneAt: Mock<SandboxPaneManagerPort['newLocalPaneAt']>
  newSandboxedPane: Mock<SandboxPaneManagerPort['newSandboxedPane']>
  installConversionTranscript: Mock<SandboxPaneManagerPort['installConversionTranscript']>
  replaceTabPosition: Mock<SandboxPaneManagerPort['replaceTabPosition']>
  closePane: Mock<SandboxPaneManagerPort['closePane']>
}

function makePaneManager(overrides: Partial<SandboxPaneManagerPort> = {}): SandboxPaneManagerFake {
  return {
    activeOrigin: vi.fn(() => localOrigin),
    paneOf: vi.fn((id: number) => (id === 1 ? oldPane : undefined)),
    tabOf: vi.fn(() => ({ id: 'wire-1' })),
    captureConversionTranscript: vi.fn(() => null),
    newLocalPaneAt: vi.fn(() => ({ pane: newPane, created: Promise.resolve(true) })),
    newSandboxedPane: vi.fn(() => ({ pane: newPane, created: Promise.resolve(true) })),
    installConversionTranscript: vi.fn(() => Promise.resolve(true)),
    replaceTabPosition: vi.fn(),
    closePane: vi.fn(),
    ...overrides,
  } as SandboxPaneManagerFake
}

function makeDeps(overrides: Partial<SandboxConversionDeps> = {}): {
  deps: SandboxConversionDeps
  inFlight: { value: boolean }
  paneManager: SandboxPaneManagerFake
} {
  const inFlight = { value: false }
  const paneManager = (overrides.paneManager ?? makePaneManager()) as SandboxPaneManagerFake
  const deps: SandboxConversionDeps = {
    shieldInput: () => ({
      enabled: true,
      status: { available: true, backend: 'landlock' },
      origin: localOrigin,
      sandboxed: false,
    }),
    inFlight: () => inFlight.value,
    setInFlight: (value: boolean) => {
      inFlight.value = value
    },
    paneManager,
    getSandboxState: vi.fn(() =>
      Promise.resolve({
        enabled: true,
        status: { available: true, backend: 'landlock' } satisfies SandboxStatus,
      }),
    ),
    getSnapshot: vi.fn(() => Promise.resolve({ values: {}, revision: 7 })),
    getProfile: vi.fn(() =>
      Promise.resolve({
        source: 'standard' as const,
        revision: 7,
        writablePaths: [],
        readOnlyPaths: [],
      }),
    ),
    openDirectory: vi.fn(() => Promise.resolve({ path: '/repo' })),
    showPermissions: vi.fn(() =>
      Promise.resolve({
        addWritable: [],
        removeWritable: [],
        addReadOnly: [],
        removeReadOnly: [],
      }),
    ),
    reportOpenError: vi.fn(),
    reportConversionError: vi.fn(),
    reportRefusal: vi.fn(),
    ...overrides,
  }
  return { deps, inFlight, paneManager }
}

describe('createSandboxConvertController.runCommand', () => {
  it('leaves non-commands alone', () => {
    const { deps } = makeDeps()
    const controller = createSandboxConvertController(deps)
    expect(controller.runCommand('echo hi')).toEqual({ kind: 'notHandled' })
    expect(controller.runCommand('/sandbox x')).toEqual({ kind: 'notHandled' })
    expect(controller.runCommand('/Sandbox')).toEqual({ kind: 'notHandled' })
  })

  it('refuses when a conversion is already in flight', () => {
    const { deps, inFlight } = makeDeps()
    inFlight.value = true
    const controller = createSandboxConvertController(deps)
    const outcome = controller.runCommand('/sandbox')
    expect(outcome).toEqual({
      kind: 'refused',
      reason: 'Sandbox conversion is already in progress',
    })
    expect(deps.reportRefusal).toHaveBeenCalledWith('Sandbox conversion is already in progress')
  })

  it('refuses with the shield reason when the surface is not eligible', () => {
    const { deps } = makeDeps({
      shieldInput: () => ({
        enabled: true,
        status: { available: true },
        origin: { ...localOrigin, cwdVerified: false },
        sandboxed: false,
      }),
    })
    const controller = createSandboxConvertController(deps)
    const outcome = controller.runCommand('/sandbox')
    expect(outcome).toEqual({ kind: 'refused', reason: 'Wait for the shell to report its folder' })
    expect(deps.reportRefusal).toHaveBeenCalledWith('Wait for the shell to report its folder')
  })

  it('refuses when the feature is disabled', () => {
    const { deps } = makeDeps({
      shieldInput: () => ({
        enabled: false,
        status: null,
        origin: localOrigin,
        sandboxed: false,
      }),
    })
    const controller = createSandboxConvertController(deps)
    expect(controller.runCommand('/sandbox')).toEqual({
      kind: 'refused',
      reason: 'Sandbox is not enabled',
    })
  })

  it('consumes a ready apply and starts the conversion', async () => {
    const { deps, paneManager, inFlight } = makeDeps()
    const controller = createSandboxConvertController(deps)
    expect(controller.runCommand('/sandbox')).toEqual({ kind: 'consumed' })
    expect(inFlight.value).toBe(true)
    // Drain the apply flow (dialog → newSandboxedPane → install → replace).
    await vi.waitFor(() => expect(paneManager.newSandboxedPane).toHaveBeenCalledTimes(1))
    await vi.waitFor(() => expect(inFlight.value).toBe(false))
    expect(paneManager.newSandboxedPane).toHaveBeenCalledWith(
      '/repo',
      expect.objectContaining({ settingsRevision: 7 }),
    )
    expect(paneManager.replaceTabPosition).toHaveBeenCalledWith(1, 2)
    expect(paneManager.closePane).toHaveBeenCalledWith(oldPane)
  })

  it('consumes a ready remove and starts the removal', async () => {
    const { deps, paneManager, inFlight } = makeDeps({
      shieldInput: () => ({
        enabled: true,
        status: { available: true },
        origin: localOrigin,
        sandboxed: true,
      }),
    })
    const controller = createSandboxConvertController(deps)
    expect(controller.runCommand('/sandbox')).toEqual({ kind: 'consumed' })
    await vi.waitFor(() => expect(paneManager.newLocalPaneAt).toHaveBeenCalledTimes(1))
    await vi.waitFor(() => expect(inFlight.value).toBe(false))
    expect(paneManager.newLocalPaneAt).toHaveBeenCalledWith('/repo')
    expect(paneManager.newSandboxedPane).not.toHaveBeenCalled()
    expect(paneManager.replaceTabPosition).toHaveBeenCalledWith(1, 2)
    expect(paneManager.closePane).toHaveBeenCalledWith(oldPane)
  })
})

describe('createSandboxConvertController.toggle', () => {
  it('is a no-op while a conversion is in flight', async () => {
    const { deps, inFlight, paneManager } = makeDeps()
    inFlight.value = true
    const controller = createSandboxConvertController(deps)
    await controller.toggle()
    expect(paneManager.newSandboxedPane).not.toHaveBeenCalled()
    expect(paneManager.newLocalPaneAt).not.toHaveBeenCalled()
  })

  it('shows the permissions dialog on apply, but never on remove', async () => {
    const apply = makeDeps()
    await createSandboxConvertController(apply.deps).toggle()
    expect(apply.deps.showPermissions).toHaveBeenCalledTimes(1)

    const remove = makeDeps({
      shieldInput: () => ({
        enabled: true,
        status: { available: true },
        origin: localOrigin,
        sandboxed: true,
      }),
    })
    await createSandboxConvertController(remove.deps).toggle()
    expect(remove.deps.showPermissions).not.toHaveBeenCalled()
  })

  it('relaunches a sandboxed tab with a newly confirmed sandbox grant', async () => {
    const relaunch = makeDeps({
      shieldInput: () => ({
        enabled: true,
        status: { available: true },
        origin: localOrigin,
        sandboxed: true,
      }),
    })

    await createSandboxConvertController(relaunch.deps).relaunch()

    expect(relaunch.deps.showPermissions).toHaveBeenCalledTimes(1)
    expect(relaunch.paneManager.newSandboxedPane).toHaveBeenCalledTimes(1)
    expect(relaunch.paneManager.newLocalPaneAt).not.toHaveBeenCalled()
    expect(relaunch.paneManager.closePane).toHaveBeenCalledWith(oldPane)
  })

  it('reports the backend reason when the fresh status is unavailable', async () => {
    const unavailable: SandboxStatus = {
      available: false,
      backend: 'landlock',
      reason: 'landlock-abi-too-old',
      detail: 'kernel Landlock ABI 2 is below the required floor of 3',
    }
    const { deps, paneManager } = makeDeps({
      getSandboxState: vi.fn(() => Promise.resolve({ enabled: true, status: unavailable })),
    })
    await createSandboxConvertController(deps).toggle()
    expect(deps.reportOpenError).toHaveBeenCalledWith(
      'Sandbox unavailable (kernel Landlock ABI 2 is below the required floor of 3)',
    )
    expect(paneManager.newSandboxedPane).not.toHaveBeenCalled()
  })
})

describe('conversion failure preservation', () => {
  it('closes the replacement and keeps the source when the transcript install fails', async () => {
    const { deps, paneManager } = makeDeps({
      paneManager: makePaneManager({
        installConversionTranscript: vi.fn(() => Promise.resolve(false)),
      }),
    })
    await createSandboxConvertController(deps).toggle()
    await vi.waitFor(() => expect(deps.reportConversionError).toHaveBeenCalledTimes(1))
    expect(paneManager.closePane).toHaveBeenCalledWith(newPane)
    expect(paneManager.closePane).not.toHaveBeenCalledWith(oldPane)
    expect(paneManager.replaceTabPosition).not.toHaveBeenCalled()
  })

  it('clears the in-flight guard even when the replace source close is the last step', async () => {
    const { deps, inFlight } = makeDeps()
    await createSandboxConvertController(deps).toggle()
    await vi.waitFor(() => expect(inFlight.value).toBe(false))
  })

  it('reports a failed replacement create instead of leaving an unhandled rejection', async () => {
    const { deps } = makeDeps({
      paneManager: makePaneManager({
        newSandboxedPane: vi.fn(() => ({
          pane: newPane,
          created: Promise.reject(new Error('boom')),
        })),
      }),
    })
    const controller = createSandboxConvertController(deps)

    await expect(controller.toggle()).resolves.toBeUndefined()
    await vi.waitFor(() => expect(deps.reportConversionError).toHaveBeenCalledTimes(1))
  })
})
