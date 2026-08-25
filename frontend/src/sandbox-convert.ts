/**
 * sandbox-convert — the ONE controller that turns the active terminal into
 * (or back from) a filesystem-sandboxed shell (design §5.2).
 *
 * Both entry points — the sidebar shield and the typed `/sandbox` command —
 * call into this controller, and the statistics tab's future relaunch
 * decision reuses `toggle`. No conversion logic lives in editor or statistics
 * code: eligibility, the complete `SandboxStatus`, the one in-flight guard,
 * and the apply/remove flows all live here.
 */
import type { SandboxLaunch, SandboxStatus } from './ipc'
import type { ActiveOrigin } from './pane-content'
import { shieldState, type ShieldStateInput } from './sandbox-shield'
import { openSandboxedShell } from './sandbox-open'
import type {
  SandboxPermissionsOptions,
  SandboxPermissionsResult,
} from './sandbox-permissions-dialog'
import type { ConversionTranscript } from './terminal-content'
import { recognizeSandboxCommand, type InternalCommandOutcome } from './sandbox-command'

/** The minimal pane surface the controller touches. `PaneManager` satisfies it
 *  structurally; tests substitute a fake. */
export interface SandboxPane {
  readonly id: number
  readonly wireId: string
}

export interface SandboxPaneManagerPort {
  activeOrigin(): ActiveOrigin | null
  paneOf(paneId: number): SandboxPane | undefined
  tabOf(paneId: number): unknown
  captureConversionTranscript(paneId: number): ConversionTranscript | null
  newLocalPaneAt(cwd: string): { pane: SandboxPane; created: Promise<boolean> }
  newSandboxedPane(
    workspace: string,
    launch: SandboxLaunch,
  ): {
    pane: SandboxPane
    created: Promise<boolean>
  }
  installConversionTranscript(
    paneId: number,
    transcript: ConversionTranscript | null,
    boundaryLabel: string,
  ): Promise<boolean>
  replaceTabPosition(oldPaneId: number, newPaneId: number): void
  closePane(pane: SandboxPane): void
}

export interface SandboxConversionDeps {
  /** Reactive eligibility inputs for the active tab (the shield's inputs). */
  shieldInput(this: void): ShieldStateInput
  /** The one in-flight guard, shared by the shield, `/sandbox`, and relaunch. */
  inFlight(this: void): boolean
  setInFlight(this: void, value: boolean): void
  paneManager: SandboxPaneManagerPort
  getSandboxState(this: void): Promise<{ enabled: boolean; status: SandboxStatus | null }>
  getSnapshot(this: void): Promise<{ values: Record<string, unknown>; revision: number }>
  getProfile(
    this: void,
    paneId: string,
  ): Promise<{
    source: 'standard' | 'workspace'
    revision: number
    writablePaths: string[]
    readOnlyPaths: string[]
  }>
  openDirectory(this: void): Promise<{ path: string }>
  showPermissions(
    this: void,
    options: SandboxPermissionsOptions,
  ): Promise<SandboxPermissionsResult | null>
  reportOpenError(this: void, message: string): void
  reportConversionError(this: void): void
  /** Report a `/sandbox` refusal reason (the draft is kept). */
  reportRefusal(this: void, message: string): void
}

export interface SandboxConvertController {
  /** Convert the active tab (apply or remove). */
  toggle(): Promise<void>
  /** Replace an active sandboxed tab with a newly confirmed sandbox grant. */
  relaunch(): Promise<void>
  /** The typed `/sandbox` command entry: a closed outcome. */
  runCommand(doc: string): InternalCommandOutcome
}

export function createSandboxConvertController(
  deps: SandboxConversionDeps,
): SandboxConvertController {
  return {
    toggle: () => convert(deps, 'toggle'),
    relaunch: () => convert(deps, 'relaunch'),
    runCommand: (doc) => runCommand(deps, doc),
  }
}

/** The `/sandbox` decision: recognize, guard, decide, and fire the conversion.
 *  Synchronous on purpose — the editor needs the outcome before the handoff,
 *  and the conversion itself starts in the same turn (its first `await` is
 *  inside `convert`). */
function runCommand(deps: SandboxConversionDeps, doc: string): InternalCommandOutcome {
  if (!recognizeSandboxCommand(doc)) return { kind: 'notHandled' }
  if (deps.inFlight()) {
    return refuse(deps, 'Sandbox conversion is already in progress')
  }
  const state = shieldState(deps.shieldInput())
  if (state.kind === 'hidden') {
    return refuse(deps, 'Sandbox is not enabled')
  }
  if (state.kind === 'disabled') {
    return refuse(deps, state.reason)
  }
  void convert(deps, 'toggle')
  return { kind: 'consumed' }
}

function refuse(deps: SandboxConversionDeps, reason: string): InternalCommandOutcome {
  deps.reportRefusal(reason)
  return { kind: 'refused', reason }
}

/** Convert the active tab. Relaunch is accepted only for an already-sandboxed
 *  tab and applies a newly confirmed profile instead of removing sandbox. */
async function convert(deps: SandboxConversionDeps, mode: 'toggle' | 'relaunch'): Promise<void> {
  if (deps.inFlight()) return
  const state = shieldState(deps.shieldInput())
  if (state.kind !== 'ready') return
  if (mode === 'relaunch' && state.action !== 'remove') return
  const source = deps.paneManager.activeOrigin()
  const oldPane = source ? deps.paneManager.paneOf(source.paneId) : undefined
  if (!source || !oldPane || !deps.paneManager.tabOf(source.paneId)) return

  deps.setInFlight(true)
  try {
    if (state.action === 'remove' && mode === 'toggle') {
      await removeSandbox(deps, oldPane, state.workspace)
    } else {
      await applySandbox(deps, oldPane, state.workspace)
    }
  } catch {
    // A durable-create or transcript-install rejection must surface as a
    // visible toast, never as an unhandled promise (the `/sandbox` path
    // fire-and-forgets convert).
    deps.reportConversionError()
  } finally {
    deps.setInFlight(false)
  }
}

/** Apply flow: fresh settings + status → permissions dialog → sandbox pane →
 *  durable create → transcript install → strip replacement → source close. */
async function applySandbox(
  deps: SandboxConversionDeps,
  oldPane: SandboxPane,
  workspace: string,
): Promise<void> {
  let state: { enabled: boolean; status: SandboxStatus | null }
  try {
    state = await deps.getSandboxState()
  } catch (err) {
    deps.reportOpenError(err instanceof Error ? err.message : 'sandbox status unavailable')
    return
  }
  if (!state.enabled) return
  if (!state.status?.available) {
    const reason = state.status?.detail || state.status?.reason || 'status-unavailable'
    deps.reportOpenError(`Sandbox unavailable (${reason})`)
    return
  }

  let conversion: Promise<void> | undefined
  await openSandboxedShell(
    {
      getSnapshot: () => deps.getSnapshot(),
      getProfile: (paneId) => deps.getProfile(paneId),
      openDirectory: () => deps.openDirectory(),
      showPermissions: (options) => deps.showPermissions(options),
      newSandboxedTab: (_workspace, launch) => {
        conversion = finishApply(deps, oldPane, _workspace, launch)
      },
      reportError: (message) => deps.reportOpenError(message),
    },
    { workspace, paneId: oldPane.wireId },
  )
  // The replacement is launched by the permissions flow's callback, so its
  // rejection is not caught by openSandboxedShell. Consume it here and report
  // it visibly before the `/sandbox` fire-and-forget can observe a rejection.
  try {
    await conversion
  } catch {
    deps.reportConversionError()
  }
}

/** Remove flow: ordinary pane → durable create → transcript install → strip
 *  replacement → source close. No permissions dialog. */
async function removeSandbox(
  deps: SandboxConversionDeps,
  oldPane: SandboxPane,
  workspace: string,
): Promise<void> {
  const transcript = deps.paneManager.captureConversionTranscript(oldPane.id)
  const made = deps.paneManager.newLocalPaneAt(workspace)
  const created = await made.created
  const installed =
    created &&
    (await deps.paneManager.installConversionTranscript(
      made.pane.id,
      transcript,
      'Sandbox removed — new shell',
    ))
  if (!installed) {
    if (created) {
      void deps.paneManager.closePane(made.pane)
      deps.reportConversionError()
    }
    return
  }
  deps.paneManager.replaceTabPosition(oldPane.id, made.pane.id)
  void deps.paneManager.closePane(oldPane)
}

/** The tail of the apply flow, after the permissions dialog resolves. */
async function finishApply(
  deps: SandboxConversionDeps,
  oldPane: SandboxPane,
  workspace: string,
  launch: SandboxLaunch,
): Promise<void> {
  const transcript = deps.paneManager.captureConversionTranscript(oldPane.id)
  const made = deps.paneManager.newSandboxedPane(workspace, launch)
  const created = await made.created
  const installed =
    created &&
    (await deps.paneManager.installConversionTranscript(
      made.pane.id,
      transcript,
      'Sandbox enabled — new shell',
    ))
  if (!installed) {
    if (created) {
      void deps.paneManager.closePane(made.pane)
      deps.reportConversionError()
    }
    return
  }
  deps.paneManager.replaceTabPosition(oldPane.id, made.pane.id)
  void deps.paneManager.closePane(oldPane)
}
