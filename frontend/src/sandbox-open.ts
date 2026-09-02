/**
 * sandbox-open — the Quick Connect "Sandboxed shell…" flow (ADR-0037).
 *
 * Extracted from the composition root so the one flow that turns a picker and
 * a permission dialog into a new tab is testable without booting `main()`. The
 * backend is the sole policy author: this reads one fresh settings snapshot,
 * shows the permission dialog seeded from both persisted baselines, and
 * forwards only the canonical workspace plus the confirmed class-scoped deltas
 * — never a baseline or an effective policy root.
 */
import type { SandboxLaunch } from './ipc'
import type {
  SandboxPermissionsOptions,
  SandboxPermissionsResult,
} from './sandbox-permissions-dialog'

export interface SandboxOpenFlowDeps {
  getSnapshot(): Promise<{ values: Record<string, unknown>; revision: number }>
  openDirectory(): Promise<{ path: string }>
  showPermissions(options: SandboxPermissionsOptions): Promise<SandboxPermissionsResult | null>
  newSandboxedTab(workspace: string, launch: SandboxLaunch): void
  reportError(message: string): void
  getProfile?: (paneId: string) => Promise<unknown>
}

/** The settings key carrying the persisted read-write baseline. Named here,
 *  deliberately — a consumer of a specific setting has to name what it
 *  consumes (the same rule that names PLACEMENT_KEY/THEME_KEY in main.tsx). */
const SANDBOX_WRITABLE_PATHS_KEY = 'sandbox.allowedWritablePaths'

/** The settings key carrying the persisted read-only baseline (ADR-0036 §3.1). */
const SANDBOX_READ_ONLY_PATHS_KEY = 'sandbox.allowedReadOnlyPaths'

/**
 * Run the sandboxed-shell open flow. Any picker or dialog cancellation
 * creates no tab; a thrown failure is reported through `reportError`.
 */
export async function openSandboxedShell(
  deps: SandboxOpenFlowDeps,
  request?: { workspace?: string; paneId?: string },
): Promise<void> {
  try {
    const snap = await deps.getSnapshot()
    const rawWritable = snap.values[SANDBOX_WRITABLE_PATHS_KEY]
    const rawReadOnly = snap.values[SANDBOX_READ_ONLY_PATHS_KEY]
    const baselineWritable = Array.isArray(rawWritable)
      ? rawWritable.filter((x): x is string => typeof x === 'string')
      : []
    const baselineReadOnly = Array.isArray(rawReadOnly)
      ? rawReadOnly.filter((x): x is string => typeof x === 'string')
      : []
    let profile: {
      source?: unknown
      revision?: unknown
      writablePaths?: unknown
      readOnlyPaths?: unknown
    } = {}
    if (request?.paneId && deps.getProfile) {
      const resolved = await deps.getProfile(request.paneId)
      if (resolved && typeof resolved === 'object') profile = resolved
    }
    const profileWritable = Array.isArray(profile.writablePaths)
      ? profile.writablePaths.filter((x): x is string => typeof x === 'string')
      : baselineWritable
    const profileReadOnly = Array.isArray(profile.readOnlyPaths)
      ? profile.readOnlyPaths.filter((x): x is string => typeof x === 'string')
      : baselineReadOnly
    const profileRevision =
      profile.source === 'workspace' && typeof profile.revision === 'number'
        ? profile.revision
        : null
    const workspace = request?.workspace ? { path: request.workspace } : await deps.openDirectory()
    if (!workspace.path) return
    const result = await deps.showPermissions({
      workspace: workspace.path,
      baselineWritable: profileWritable,
      baselineReadOnly: profileReadOnly,
      openDirectory: () => deps.openDirectory(),
    })
    if (!result) return
    deps.newSandboxedTab(workspace.path, {
      mode: 'enforce',
      profileRevision,
      settingsRevision: snap.revision,
      addWritable: result.addWritable,
      removeWritable: result.removeWritable,
      addReadOnly: result.addReadOnly,
      removeReadOnly: result.removeReadOnly,
    })
  } catch (err) {
    deps.reportError(err instanceof Error ? err.message : String(err))
  }
}
